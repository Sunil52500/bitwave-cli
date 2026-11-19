package operations

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	op "github.com/bitwave-io/bitwave-cli/internal/operation"
)

// hardCloseTestServer answers api-svc's GraphQL endpoint with reply and records
// every request body it was sent.
func hardCloseTestServer(t *testing.T, reply string) *[]map[string]any {
	t.Helper()
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/graphql" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode GraphQL body: %v", err)
		}
		bodies = append(bodies, body)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(server.Close)
	t.Setenv("BITWAVE_BASE_URL_CORE", server.URL)
	t.Setenv("BITWAVE_TOKEN", "token")
	return &bodies
}

func runCloseCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := testDefinition(newCloseCmd())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestCloseHardCloseGetReportsTheLockedPeriod(t *testing.T) {
	bodies := hardCloseTestServer(t, `{"data":{"org":{"id":"org-1","timezone":"America/New_York","cutoffTime":null,"hardCloseDate":"2026-09-30"}}}`)

	out, err := runCloseCommand(t, "hard-close", "get", "--org", "org-1")
	if err != nil {
		t.Fatal(err)
	}
	var got hardCloseStatus
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if !got.Locked || got.HardCloseDate == nil || *got.HardCloseDate != "2026-09-30" || got.Timezone != "America/New_York" || got.Organization != "org-1" {
		t.Fatalf("status = %+v", got)
	}
	if got.CutoffTime != "" {
		t.Fatalf("cutoffTime = %q, want none", got.CutoffTime)
	}
	if !strings.Contains(got.LockedThrough, "2026-09-30") || !strings.Contains(got.LockedThrough, "end of the day") ||
		!strings.Contains(got.LockedThrough, "America/New_York") {
		t.Fatalf("lockedThrough = %q", got.LockedThrough)
	}

	if len(*bodies) != 1 {
		t.Fatalf("requests = %d, want 1", len(*bodies))
	}
	query, _ := (*bodies)[0]["query"].(string)
	if !strings.Contains(query, "hardCloseDate") || graphqlMayWrite(query) {
		t.Fatalf("query = %q, want a read of hardCloseDate", query)
	}
	variables := (*bodies)[0]["variables"].(map[string]any)
	if variables["orgId"] != "org-1" || variables["allowCache"] != false {
		t.Fatalf("variables = %v, want org-1 read uncached", variables)
	}
}

func TestCloseHardCloseGetUsesTheAccountingDayCutoff(t *testing.T) {
	hardCloseTestServer(t, `{"data":{"org":{"id":"org-1","timezone":"Asia/Singapore","cutoffTime":"16:00","hardCloseDate":"2026-06-30"}}}`)

	out, err := runCloseCommand(t, "hard-close", "get", "--org", "org-1")
	if err != nil {
		t.Fatal(err)
	}
	var got hardCloseStatus
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.CutoffTime != "16:00" || !strings.Contains(got.LockedThrough, "16:00 on 2026-06-30") ||
		strings.Contains(got.LockedThrough, "end of the day") {
		t.Fatalf("status = %+v", got)
	}
}

func TestCloseHardCloseGetReportsAnUnlockedOrganization(t *testing.T) {
	hardCloseTestServer(t, `{"data":{"org":{"id":"org-1","timezone":"UTC","cutoffTime":null,"hardCloseDate":null}}}`)

	out, err := runCloseCommand(t, "hard-close", "get", "--org", "org-1")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got["locked"] != false || got["hardCloseDate"] != nil {
		t.Fatalf("status = %v, want unlocked with a null date", got)
	}
	if lockedThrough, _ := got["lockedThrough"].(string); !strings.Contains(lockedThrough, "No period is locked") {
		t.Fatalf("lockedThrough = %v", got["lockedThrough"])
	}
}

func TestCloseHardCloseGetFailsOnAGraphQLError(t *testing.T) {
	hardCloseTestServer(t, `{"errors":[{"message":"Forbidden"}],"data":{"org":null}}`)

	if _, err := runCloseCommand(t, "hard-close", "get", "--org", "org-1"); err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Fatalf("error = %v, want the GraphQL error", err)
	}
}

func TestCloseHardCloseGetFailsWhenTheOrganizationIsMissing(t *testing.T) {
	hardCloseTestServer(t, `{"data":{"org":null}}`)

	if _, err := runCloseCommand(t, "hard-close", "get", "--org", "org-1"); err == nil {
		t.Fatal("a missing organization must not read as unlocked")
	}
}

// api-svc refuses a lock over uncategorized transactions with a GraphQL error
// on HTTP 200. The setter must fail rather than print it as a success.
func TestCloseHardCloseSetFailsWhenApiSvcRefusesTheLock(t *testing.T) {
	hardCloseTestServer(t, `{"errors":[{"message":"Cannot lock the period: 3 uncategorized transactions on or before 2026-09-30"}],"data":{"updateOrg":null}}`)

	_, err := runCloseCommand(t, "hard-close", "--org", "org-1", "--end-date", "2026-09-30", "--yes")
	if err == nil || !strings.Contains(err.Error(), "uncategorized transactions") {
		t.Fatalf("error = %v, want api-svc's refusal", err)
	}
}

func TestCloseHardCloseSetSendsTheDate(t *testing.T) {
	bodies := hardCloseTestServer(t, `{"data":{"updateOrg":{"id":"org-1"}}}`)

	if _, err := runCloseCommand(t, "hard-close", "--org", "org-1", "--end-date", "2026-09-30", "--yes"); err != nil {
		t.Fatal(err)
	}
	variables := (*bodies)[0]["variables"].(map[string]any)
	if variables["orgId"] != "org-1" || variables["hardCloseDate"] != "2026-09-30" {
		t.Fatalf("variables = %v", variables)
	}
}

// Hosted agents find tools by the words in their names and descriptions, and a
// person asks whether a period is "locked" or "closed", not for a hard-close
// date. Both tools must say what the date does in those words.
func TestHardCloseToolsDescribeThePeriodLock(t *testing.T) {
	descriptors, err := op.Descriptors(NewRoot())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]op.Descriptor{}
	for _, d := range descriptors {
		byName[d.Name] = d
	}
	for name, words := range map[string][]string{
		"bitwave_close_hard_close_get": {"read", "locked", "period", "accounting", "close", "timezone"},
		"bitwave_close_hard_close":     {"set", "lock", "period", "accounting", "uncategorized", "earlier", "bitwave close hard-close get"},
	} {
		d, ok := byName[name]
		if !ok {
			t.Errorf("missing tool %s", name)
			continue
		}
		description := strings.ToLower(d.Description)
		for _, word := range words {
			if !strings.Contains(description, word) {
				t.Errorf("%s description lacks %q: %s", name, word, d.Description)
			}
		}
	}
}
