package operations

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// combineTestServer answers the org settings read and records every PUT to
// combine or uncombine, replying with the canned body for that path.
func combineTestServer(t *testing.T, replies map[string]string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("request lost its token: %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v3/orgs/org-1":
			_, _ = w.Write([]byte(`{"id":"org-1","timezone":"UTC"}`))
		case r.Method == http.MethodPut:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode %s body: %v", r.URL.Path, err)
			}
			bodies = append(bodies, body)
			reply, ok := replies[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(reply))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("BITWAVE_BASE_URL_CORE", server.URL)
	t.Setenv("BITWAVE_TOKEN", "token")
	return server, &bodies
}

func runMutationCommand(t *testing.T, args ...string) (mutationEnvelope, string, error) {
	t.Helper()
	cmd := testDefinition(newOrgTransactionsCmd())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	var envelope mutationEnvelope
	if out.Len() > 0 {
		if jsonErr := json.Unmarshal(out.Bytes(), &envelope); jsonErr != nil {
			t.Fatalf("output is not an envelope: %v\n%s", jsonErr, out.String())
		}
	}
	return envelope, out.String(), err
}

func TestCombineDryRunPrintsTheExactRequestWithoutContactingBitwave(t *testing.T) {
	t.Setenv("BITWAVE_BASE_URL_CORE", "http://127.0.0.1:9") // nothing listens: any request fails loudly
	t.Setenv("BITWAVE_TOKEN", "token")
	envelope, out, err := runMutationCommand(t, "combine", "BASE.0xabc?id=1", "BASE.0xabc", "--org", "org-1", "--dry-run", "--json")
	if err != nil {
		t.Fatal(err, out)
	}
	if envelope.Status != "preview" || envelope.Operation != "combine" || !envelope.DryRun {
		t.Fatalf("envelope = %#v", envelope)
	}
	request := envelope.Request.(map[string]any)
	if request["method"] != "PUT" || request["path"] != "/v3/orgs/org-1/transactions/combine" {
		t.Fatalf("request = %#v", request)
	}
	ids := request["body"].(map[string]any)["txnIds"].([]any)
	if len(ids) != 2 || ids[0] != "BASE.0xabc?id=1" || ids[1] != "BASE.0xabc" {
		t.Fatalf("txnIds = %#v", ids)
	}
}

func TestCombineRefusesWithoutYes(t *testing.T) {
	_, bodies := combineTestServer(t, nil)
	envelope, _, err := runMutationCommand(t, "combine", "txn-1", "txn-2", "--org", "org-1", "--json")
	if err == nil || !strings.Contains(err.Error(), "without --yes") {
		t.Fatalf("err = %v", err)
	}
	if envelope.Status != "error" || envelope.Error == nil || envelope.Error.Code != "confirmation_required" {
		t.Fatalf("envelope = %#v", envelope)
	}
	if len(*bodies) != 0 {
		t.Fatal("a refused combine must not reach the server")
	}
}

func TestCombineNeedsTwoDistinctTransactions(t *testing.T) {
	_, bodies := combineTestServer(t, nil)
	// The same id twice collapses to one after deduplication.
	_, _, err := runMutationCommand(t, "combine", "txn-1", "txn-1", "--org", "org-1", "--yes", "--json")
	if err == nil || !strings.Contains(err.Error(), "two distinct") {
		t.Fatalf("err = %v", err)
	}
	if len(*bodies) != 0 {
		t.Fatal("an invalid combine must not reach the server")
	}
}

func TestCombineSendsTheIdsAndReportsTheParent(t *testing.T) {
	_, bodies := combineTestServer(t, map[string]string{"/v3/orgs/org-1/transactions/combine": `{"id":"parent-1"}`})
	envelope, out, err := runMutationCommand(t, "combine", "BASE.0xabc?id=1", "BASE.0xabc", "BASE.0xabc", "--org", "org-1", "--yes", "--json")
	if err != nil {
		t.Fatal(err, out)
	}
	if len(*bodies) != 1 {
		t.Fatalf("expected one combine request, got %d", len(*bodies))
	}
	ids := (*bodies)[0]["txnIds"].([]any)
	if len(ids) != 2 {
		t.Fatalf("ids must be deduplicated before sending: %v", ids)
	}
	if envelope.Status != "success" {
		t.Fatalf("envelope = %#v", envelope)
	}
	result := envelope.Result.(map[string]any)
	if result["combinedTransactionId"] != "parent-1" {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(result["note"].(string), "uncombine parent-1") {
		t.Fatalf("the result must say how to reverse the combine: %#v", result)
	}
}

func TestCombineWithoutAParentIdIsAnError(t *testing.T) {
	combineTestServer(t, map[string]string{"/v3/orgs/org-1/transactions/combine": `{}`})
	envelope, _, err := runMutationCommand(t, "combine", "txn-1", "txn-2", "--org", "org-1", "--yes", "--json")
	if err == nil || !strings.Contains(err.Error(), "no combined transaction id") {
		t.Fatalf("err = %v", err)
	}
	if envelope.Status != "error" {
		t.Fatalf("envelope = %#v", envelope)
	}
}

func TestUncombineReportsEveryParentAndFailsOnAPartialResult(t *testing.T) {
	_, bodies := combineTestServer(t, map[string]string{
		"/v3/orgs/org-1/transactions/uncombine": `[{"txId":"parent-1","status":"fulfilled"},{"txId":"parent-2","status":"rejected"}]`,
	})
	envelope, _, err := runMutationCommand(t, "uncombine", "parent-1", "parent-2", "--org", "org-1", "--yes", "--json")
	if err == nil || !strings.Contains(err.Error(), "1 succeeded, 1 failed") {
		t.Fatalf("err = %v", err)
	}
	if len(*bodies) != 1 || len((*bodies)[0]["txnIds"].([]any)) != 2 {
		t.Fatalf("bodies = %#v", *bodies)
	}
	if envelope.Status != "partial_failure" {
		t.Fatalf("envelope = %#v", envelope)
	}
	result := envelope.Result.(map[string]any)
	if result["succeeded"].(float64) != 1 || len(result["failed"].([]any)) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestUncombineSucceedsWhenEveryParentIsFulfilled(t *testing.T) {
	combineTestServer(t, map[string]string{
		"/v3/orgs/org-1/transactions/uncombine": `[{"txId":"parent-1","status":"fulfilled"}]`,
	})
	envelope, out, err := runMutationCommand(t, "uncombine", "parent-1", "--org", "org-1", "--yes", "--json")
	if err != nil {
		t.Fatal(err, out)
	}
	if envelope.Status != "success" || envelope.Operation != "uncombine" {
		t.Fatalf("envelope = %#v", envelope)
	}
}

func TestUncombineDryRunNeedsNoConfirmation(t *testing.T) {
	envelope, _, err := runMutationCommand(t, "uncombine", "parent-1", "--org", "org-1", "--dry-run", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Status != "preview" || envelope.Request.(map[string]any)["path"] != "/v3/orgs/org-1/transactions/uncombine" {
		t.Fatalf("envelope = %#v", envelope)
	}
}

// batchCombineServer answers each combine with a parent named after the
// group's first id, or a 409 for groups whose first id is in refuse.
func batchCombineServer(t *testing.T, refuse map[string]bool) *[][]string {
	t.Helper()
	var sent [][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v3/orgs/org-1/transactions/combine" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var body struct {
			TxnIDs []string `json:"txnIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		sent = append(sent, body.TxnIDs)
		if refuse[body.TxnIDs[0]] {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"already combined"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"parent-` + body.TxnIDs[0] + `"}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("BITWAVE_BASE_URL_CORE", server.URL)
	t.Setenv("BITWAVE_TOKEN", "token")
	return &sent
}

func TestCombineGroupsRunOneRequestPerGroupInOneConfirmedCall(t *testing.T) {
	sent := batchCombineServer(t, nil)
	envelope, out, err := runMutationCommand(t, "combine", "--group", "a1,a2", "--group", " b1 , b2 ,b3", "--org", "org-1", "--yes", "--json")
	if err != nil {
		t.Fatal(err, out)
	}
	if len(*sent) != 2 || strings.Join((*sent)[1], ",") != "b1,b2,b3" {
		t.Fatalf("requests = %v", *sent)
	}
	result := envelope.Result.(map[string]any)
	if envelope.Status != "success" || result["succeeded"].(float64) != 2 || result["failed"].(float64) != 0 {
		t.Fatalf("envelope = %#v", envelope)
	}
	first := result["results"].([]any)[0].(map[string]any)
	if first["combinedTransactionId"] != "parent-a1" || first["status"] != "success" {
		t.Fatalf("first result = %#v", first)
	}
}

func TestCombineGroupsReportARefusedGroupAndStillRunTheRest(t *testing.T) {
	sent := batchCombineServer(t, map[string]bool{"b1": true})
	envelope, _, err := runMutationCommand(t, "combine", "--group", "a1,a2", "--group", "b1,b2", "--group", "c1,c2", "--org", "org-1", "--yes", "--json")
	if err == nil || !strings.Contains(err.Error(), "2 succeeded, 1 failed") {
		t.Fatalf("err = %v", err)
	}
	if len(*sent) != 3 {
		t.Fatalf("every group must be attempted: %v", *sent)
	}
	if envelope.Status != "partial_failure" {
		t.Fatalf("envelope = %#v", envelope)
	}
	refused := envelope.Result.(map[string]any)["results"].([]any)[1].(map[string]any)
	if refused["status"] != "failed" || refused["error"] == "" || refused["combinedTransactionId"] != nil {
		t.Fatalf("refused group = %#v", refused)
	}
}

func TestCombineGroupsDryRunListsEveryRequest(t *testing.T) {
	envelope, out, err := runMutationCommand(t, "combine", "--group", "a1,a2", "--group", "b1,b2", "--org", "org-1", "--dry-run", "--json")
	if err != nil {
		t.Fatal(err, out)
	}
	requests := envelope.Request.([]any)
	if envelope.Status != "preview" || len(requests) != 2 {
		t.Fatalf("envelope = %#v", envelope)
	}
	if requests[1].(map[string]any)["path"] != "/v3/orgs/org-1/transactions/combine" {
		t.Fatalf("request = %#v", requests[1])
	}
}

func TestCombineGroupsValidateBeforeAnyRequest(t *testing.T) {
	sent := batchCombineServer(t, nil)
	for _, test := range []struct {
		args    []string
		message string
	}{
		{[]string{"--group", "a1"}, "needs at least two distinct"},
		{[]string{"--group", "a1,a1"}, "needs at least two distinct"},
		{[]string{"--group", "a1,a2", "--group", "a2,b2"}, "transaction a2 is in --group 1 and --group 2"},
		{[]string{"x1", "--group", "a1,a2"}, "either as arguments or with --group"},
	} {
		_, _, err := runMutationCommand(t, append([]string{"combine", "--org", "org-1", "--yes", "--json"}, test.args...)...)
		if err == nil || !strings.Contains(err.Error(), test.message) {
			t.Errorf("%v: err = %v, want %q", test.args, err, test.message)
		}
	}
	if len(*sent) != 0 {
		t.Fatalf("invalid groups must not reach the server: %v", *sent)
	}
	groups := make([]string, maxCombineGroups+1)
	for i := range groups {
		groups[i] = "--group=x" + strings.Repeat("y", i+1) + ",z" + strings.Repeat("y", i+1)
	}
	if _, _, err := runMutationCommand(t, append([]string{"combine", "--org", "org-1", "--yes", "--json"}, groups...)...); err == nil || !strings.Contains(err.Error(), "at most 100 groups") {
		t.Fatalf("over the cap: err = %v", err)
	}
}
