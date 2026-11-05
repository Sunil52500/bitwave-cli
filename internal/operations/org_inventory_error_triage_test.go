package operations

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// triagePlatform is the Bitwave platform's error-triage routes as the command
// uses them: a submit that answers 202 with a report run, and a read that
// answers the run's state and, once succeeded, its result.
type triagePlatform struct {
	t  *testing.T
	mu sync.Mutex
	// submits records every submit's query string.
	submits []string
	// reads counts the polls per report run.
	reads map[string]int
	// submitAnswer is what a submit returns.
	submitAnswer map[string]any
	// statuses is the sequence of states a read of each run walks through; the last one repeats.
	statuses map[string][]string
	// result is what a succeeded run carries.
	result map[string]any
	// readFailures is how many reads of a run fail with 503 before answering.
	readFailures map[string]int
}

func newTriagePlatform(t *testing.T) *triagePlatform {
	return &triagePlatform{
		t:            t,
		reads:        map[string]int{},
		submitAnswer: map[string]any{"reportRunId": "rr-1", "runId": "run-1", "asOfSEC": 1762387199, "status": "new", "attached": false},
		statuses:     map[string][]string{"rr-1": {"new", "running", "succeeded"}},
		result:       map[string]any{"inventoryViewId": "view-1", "runId": "run-1", "asOfSEC": 1762387199, "erroredActions": 2, "truncated": false, "groups": []any{map[string]any{"asset": "ETH", "cause": "unpriced"}}, "byCause": map[string]any{"unpriced": 2}},
		readFailures: map[string]int{},
	}
}

func (p *triagePlatform) serve() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer token" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		const base = "/orgs/org-1/inventory-views/view-1/error-triage"
		switch {
		case r.Method == http.MethodPost && r.URL.Path == base:
			p.submits = append(p.submits, r.URL.RawQuery)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(p.submitAnswer)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, base+"/"):
			id := strings.TrimPrefix(r.URL.Path, base+"/")
			if p.readFailures[id] > 0 {
				p.readFailures[id]--
				http.Error(w, "warehouse hiccup", http.StatusServiceUnavailable)
				return
			}
			states, known := p.statuses[id]
			if !known {
				http.Error(w, `{"message":"Report run not found"}`, http.StatusNotFound)
				return
			}
			n := p.reads[id]
			p.reads[id] = n + 1
			if n >= len(states) {
				n = len(states) - 1
			}
			answer := map[string]any{"reportRunId": id, "status": states[n]}
			if states[n] == "succeeded" {
				answer["result"] = p.result
			}
			_ = json.NewEncoder(w).Encode(answer)
		default:
			p.t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
}

func runTriage(t *testing.T, server *httptest.Server, args ...string) (mutationEnvelope, error) {
	t.Helper()
	t.Setenv("BITWAVE_BASE_URL_CORE", server.URL)
	t.Setenv("BITWAVE_TOKEN", "token")
	cmd := testDefinition(newOrgInventoryAPICmd())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(append([]string{"runs", "error-triage", "view-1", "--org", "org-1", "--poll-interval", "1ms"}, args...))
	err := cmd.Execute()
	var envelope mutationEnvelope
	if out.Len() > 0 {
		if decodeErr := json.Unmarshal(out.Bytes(), &envelope); decodeErr != nil {
			t.Fatalf("output is not an envelope: %v\n%s", decodeErr, out.String())
		}
	}
	return envelope, err
}

func TestInventoryErrorTriageSubmitsWaitsAndReturnsTheResult(t *testing.T) {
	platform := newTriagePlatform(t)
	server := platform.serve()
	defer server.Close()

	envelope, err := runTriage(t, server, "--as-of", "2026-11-04")
	if err != nil {
		t.Fatalf("execute: %v\n%#v", err, envelope)
	}
	if envelope.Status != "success" || envelope.Operation != "view-error-triage" || envelope.Organization != "org-1" {
		t.Fatalf("envelope = %#v", envelope)
	}
	result := envelope.Result.(map[string]any)
	if result["reportRunId"] != "rr-1" || result["status"] != "succeeded" || result["runId"] != "run-1" || result["attached"] != false {
		t.Fatalf("result = %#v", result)
	}
	triage := result["result"].(map[string]any)
	if triage["erroredActions"] != float64(2) || triage["byCause"].(map[string]any)["unpriced"] != float64(2) {
		t.Fatalf("triage = %#v", triage)
	}
	if len(platform.submits) != 1 || platform.submits[0] != "asOf=2026-11-04" {
		t.Fatalf("submits = %#v", platform.submits)
	}
	// new, running, then succeeded: three reads, no more
	if platform.reads["rr-1"] != 3 {
		t.Fatalf("reads = %d", platform.reads["rr-1"])
	}
}

func TestInventoryErrorTriageAttachesAndRefreshes(t *testing.T) {
	platform := newTriagePlatform(t)
	platform.submitAnswer = map[string]any{"reportRunId": "rr-1", "runId": "run-1", "asOfSEC": 1, "status": "running", "attached": true}
	platform.statuses["rr-1"] = []string{"succeeded"}
	server := platform.serve()
	defer server.Close()

	envelope, err := runTriage(t, server)
	if err != nil {
		t.Fatal(err)
	}
	if result := envelope.Result.(map[string]any); result["attached"] != true {
		t.Fatalf("result = %#v", result)
	}
	if _, err := runTriage(t, server, "--refresh"); err != nil {
		t.Fatal(err)
	}
	if len(platform.submits) != 2 || platform.submits[0] != "" || platform.submits[1] != "refresh=true" {
		t.Fatalf("submits = %#v", platform.submits)
	}
}

func TestInventoryErrorTriageSubmitOnlyAndReportRunRead(t *testing.T) {
	platform := newTriagePlatform(t)
	platform.statuses["rr-1"] = []string{"succeeded"}
	server := platform.serve()
	defer server.Close()

	envelope, err := runTriage(t, server, "--submit-only")
	if err != nil {
		t.Fatal(err)
	}
	if result := envelope.Result.(map[string]any); result["reportRunId"] != "rr-1" || result["status"] != "new" || result["result"] != nil {
		t.Fatalf("submit-only result = %#v", result)
	}
	if platform.reads["rr-1"] != 0 {
		t.Fatalf("submit-only polled %d times", platform.reads["rr-1"])
	}

	envelope, err = runTriage(t, server, "--report-run", "rr-1")
	if err != nil {
		t.Fatal(err)
	}
	result := envelope.Result.(map[string]any)
	if result["reportRunId"] != "rr-1" || result["status"] != "succeeded" || result["result"] == nil || result["runId"] != nil {
		t.Fatalf("report-run result = %#v", result)
	}
	if len(platform.submits) != 1 {
		t.Fatalf("a --report-run read submitted: %#v", platform.submits)
	}

	if _, err := runTriage(t, server, "--report-run", "rr-9"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("unknown run err = %v", err)
	}
	if _, err := runTriage(t, server, "--report-run", "rr-1", "--dry-run"); err == nil || !strings.Contains(err.Error(), "--report-run") {
		t.Fatalf("dry-run with report-run err = %v", err)
	}
}

func TestInventoryErrorTriageFailedRunIsAnErrorEnvelope(t *testing.T) {
	platform := newTriagePlatform(t)
	platform.statuses["rr-1"] = []string{"running", "timed-out"}
	server := platform.serve()
	defer server.Close()

	envelope, err := runTriage(t, server)
	if err == nil || !strings.Contains(err.Error(), "timed-out") {
		t.Fatalf("err = %v", err)
	}
	if envelope.Status != "error" || envelope.Error == nil || envelope.Error.Code != "triage_timed_out" || !envelope.Error.Retryable {
		t.Fatalf("envelope = %#v error = %#v", envelope, envelope.Error)
	}
	if !strings.Contains(envelope.Error.Suggestion, "--refresh") {
		t.Fatalf("suggestion = %q", envelope.Error.Suggestion)
	}
	if result := envelope.Result.(map[string]any); result["reportRunId"] != "rr-1" || result["status"] != "timed-out" {
		t.Fatalf("result = %#v", result)
	}
}

func TestInventoryErrorTriageWaitThatRunsOutOfTimeSaysHowToResume(t *testing.T) {
	platform := newTriagePlatform(t)
	platform.statuses["rr-1"] = []string{"running"}
	server := platform.serve()
	defer server.Close()

	envelope, err := runTriage(t, server, "--timeout", "20ms")
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("err = %v", err)
	}
	if envelope.Status != "error" || envelope.Error == nil || envelope.Error.Code != "triage_pending" || !envelope.Error.Retryable {
		t.Fatalf("envelope = %#v error = %#v", envelope, envelope.Error)
	}
	if !strings.Contains(envelope.Error.Suggestion, "--report-run rr-1") {
		t.Fatalf("suggestion = %q", envelope.Error.Suggestion)
	}
}

func TestInventoryErrorTriagePollRetriesPlatformHiccups(t *testing.T) {
	platform := newTriagePlatform(t)
	platform.statuses["rr-1"] = []string{"succeeded"}
	platform.readFailures["rr-1"] = 2
	server := platform.serve()
	defer server.Close()

	envelope, err := runTriage(t, server)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Status != "success" || platform.reads["rr-1"] != 1 {
		t.Fatalf("envelope = %#v reads = %d", envelope, platform.reads["rr-1"])
	}

	// more hiccups than the budget allows is final
	platform.readFailures["rr-1"] = 6
	platform.submits = nil
	if _, err := runTriage(t, server); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v", err)
	}
}

func TestInventoryErrorTriageRefusesBadFlagsBeforeContactingThePlatform(t *testing.T) {
	platform := newTriagePlatform(t)
	server := platform.serve()
	defer server.Close()

	if _, err := runTriage(t, server, "--as-of", "4 Nov 2026"); err == nil || !strings.Contains(err.Error(), "YYYY-MM-DD") {
		t.Fatalf("as-of err = %v", err)
	}
	if _, err := runTriage(t, server, "--timeout", "0s"); err == nil || !strings.Contains(err.Error(), "--timeout") {
		t.Fatalf("timeout err = %v", err)
	}
	if len(platform.submits) != 0 {
		t.Fatalf("submits = %#v", platform.submits)
	}
}
