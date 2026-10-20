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
