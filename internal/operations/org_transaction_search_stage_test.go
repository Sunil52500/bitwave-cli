package operations

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bitwave-io/bitwave-cli/internal/orgreports"
)

// The transaction UI's tabs narrow the list with a server-side stage that no
// other filter reproduces, so search must send it the way count already does.
func TestSearchSendsTheStageTheUITabsUse(t *testing.T) {
	var request orgreports.TransactionSearchRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v3/orgs/org-1":
			_, _ = w.Write([]byte(`{"id":"org-1","timezone":"UTC"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/orgs/org-1/wallets":
			_, _ = w.Write([]byte(`{"items":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v3/orgs/org-1/transactions/search":
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode search: %v", err)
			}
			_, _ = w.Write([]byte(`{"transactions":[]}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("BITWAVE_BASE_URL_CORE", server.URL)
	t.Setenv("BITWAVE_TOKEN", "token")

	cmd := testDefinition(newSearchOrgTransactionsCmd())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--org", "org-1", "--stage", " needs-categorization ", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err, out.String())
	}
	if request.Filters.Stage != "needs-categorization" {
		t.Fatalf("stage was not sent: %#v", request.Filters)
	}
	var result struct {
		Request orgreports.TransactionSearchRequest `json:"request"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Request.Filters.Stage != "needs-categorization" {
		t.Fatalf("the echoed request must show the stage: %#v", result.Request.Filters)
	}
}
