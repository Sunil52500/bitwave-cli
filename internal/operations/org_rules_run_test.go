package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	op "github.com/bitwave-io/bitwave-cli/internal/operation"
)

// ruleRunRuntime is the real operation runtime over a concrete fake of the
// platform: respond answers every request, and the fake fails the test on
// any GraphQL call, because the rules run must never post runRulesForOrg.
func ruleRunRuntime(t *testing.T, respond func(r *http.Request) (*http.Response, error)) *op.Runtime {
	t.Helper()
	runtime, err := op.NewRuntime(op.Options{
		WorkingDirectory: t.TempDir(), OrganizationID: "org-1", Token: "token", CoreBaseURL: "https://unit.invalid",
		HTTPClient: &http.Client{Transport: businessTransport(func(r *http.Request) (*http.Response, error) {
			if strings.Contains(r.URL.Path, "graphql") {
				t.Fatalf("the rules run must not use GraphQL: %s %s", r.Method, r.URL.String())
			}
			return respond(r)
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

func jsonResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Request: r, Body: io.NopCloser(strings.NewReader(body))}
}

func invokeRuleCommand(t *testing.T, runtime *op.Runtime, definition *op.Definition, flags []string) (string, error) {
	t.Helper()
	var output bytes.Buffer
	call := op.NewCall(op.WithRuntime(context.Background(), runtime), definition, nil, &output, io.Discard)
	if err := definition.Flags().Parse(flags); err != nil {
		t.Fatal(err)
	}
	err := definition.Invoke(call, nil)
	return output.String(), err
}

// The dry run shows the sync-coordinator invoke route for the organization's
// deterministic rules-workflow job, with overrideHeartbeat=true, not the
// GraphQL mutation.
func TestRuleRunDryRunPreviewsTheSyncCoordinatorInvoke(t *testing.T) {
	cmd := testDefinition(newRunRulesCmd())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--org", "org-1", "--dry-run", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var result mutationEnvelope
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode output %q: %v", out.String(), err)
	}
	if result.Status != "preview" || !result.DryRun || result.Operation != "run-rules" {
		t.Fatalf("envelope = %#v", result)
	}
	request := result.Request.(map[string]any)
	if request["url"] != "https://api.bitwave.io/v3/orgs/org-1/jobs/api-svc-rules/rules-workflow/org-1.rules-workflow/invoke?overrideHeartbeat=true" || request["method"] != "POST" || request["service"] != "sync-coordinator" {
		t.Fatalf("request = %#v", request)
	}
	if strings.Contains(out.String(), "RunRulesForOrg") {
		t.Fatalf("preview still names the GraphQL mutation: %s", out.String())
	}
}

func TestRuleRunRequiresConfirmation(t *testing.T) {
	runtime := ruleRunRuntime(t, func(r *http.Request) (*http.Response, error) {
		t.Fatalf("nothing may be sent without --yes: %s %s", r.Method, r.URL.String())
		return nil, nil
	})
	output, err := invokeRuleCommand(t, runtime, newRunRulesCmd(), []string{"--json"})
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected a confirmation refusal, got %v", err)
	}
	var result mutationEnvelope
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode %q: %v", output, err)
	}
	if result.Error == nil || result.Error.Code != "confirmation_required" {
		t.Fatalf("envelope = %s", output)
	}
}

// With --yes the operation invokes the job and reports the job run it
// started as a start, not a completion.
func TestRuleRunStartsTheRulesWorkflowJob(t *testing.T) {
	var posted *http.Request
	var body string
	runtime := ruleRunRuntime(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/v3/orgs/org-1/jobs/api-svc-rules/rules-workflow/org-1.rules-workflow/invoke" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		raw, _ := io.ReadAll(r.Body)
		posted, body = r, string(raw)
		return jsonResponse(r, http.StatusOK, `{"jobRunId":"run-1","jobId":"org-1.rules-workflow","message":"Successfully invoked job org-1.rules-workflow"}`), nil
	})
	output, err := invokeRuleCommand(t, runtime, newRunRulesCmd(), []string{"--yes", "--json"})
	if err != nil {
		t.Fatalf("%v (output: %s)", err, output)
	}
	if posted == nil || posted.URL.Query().Get("overrideHeartbeat") != "true" || posted.Header.Get("Authorization") != "Bearer token" || strings.TrimSpace(body) != "{}" {
		t.Fatalf("posted = %v body=%q", posted, body)
	}
	var result mutationEnvelope
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode %q: %v", output, err)
	}
	if result.Status != "success" || result.Operation != "run-rules" || result.Organization != "org-1" {
		t.Fatalf("envelope = %#v", result)
	}
	got := result.Result.(map[string]any)
	if got["triggered"] != true || got["asynchronous"] != true || got["jobRunId"] != "run-1" || got["jobId"] != "org-1.rules-workflow" {
		t.Fatalf("result = %#v", got)
	}
}

func TestRuleRunHumanOutputNamesTheJobRun(t *testing.T) {
	runtime := ruleRunRuntime(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(r, http.StatusOK, `{"jobRunId":"run-1","jobId":"org-1.rules-workflow","message":"ok"}`), nil
	})
	output, err := invokeRuleCommand(t, runtime, newRunRulesCmd(), []string{"--yes"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "run-1") || !strings.Contains(output, "asynchronous") {
		t.Fatalf("output = %q", output)
	}
}

// sync-coordinator declining the start (412, the runner's own reason) is an
// API error with that status and reason, never a success.
func TestRuleRunReportsADeclinedStart(t *testing.T) {
	runtime := ruleRunRuntime(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(r, http.StatusPreconditionFailed, `{"status":"FAILED_PRECONDITION","error":"job org-1.rules-workflow was not started: a run is already in progress"}`), nil
	})
	output, err := invokeRuleCommand(t, runtime, newRunRulesCmd(), []string{"--yes", "--json"})
	if err == nil || !strings.Contains(err.Error(), "was not started: a run is already in progress") {
		t.Fatalf("err = %v", err)
	}
	var result mutationEnvelope
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode %q: %v", output, err)
	}
	if result.Status == "success" || result.Error == nil || result.Error.Code != "api_error" || result.Error.HTTPStatus != http.StatusPreconditionFailed {
		t.Fatalf("envelope = %s", output)
	}
}

// The help text is the hosted tool description: it must say what the run is
// and point date-bounded runs at bulk-run and its flag.
func TestRuleRunHelpDescribesTheStandardRun(t *testing.T) {
	long := newRunRulesCmd().Long
	for _, want := range []string{"sync-coordinator", "overrideHeartbeat=true", "asynchronous", "needs no feature flag", "parallel rule processing", "--dry-run", "--yes"} {
		if !strings.Contains(long, want) {
			t.Errorf("rule run help does not mention %q", want)
		}
	}
	if strings.Contains(long, "—") {
		t.Error("rule run help contains an em dash")
	}
}

func bulkRunRuntime(t *testing.T, executeStatus int, executeBody string) *op.Runtime {
	t.Helper()
	return ruleRunRuntime(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v3/orgs/org-1":
			return jsonResponse(r, http.StatusOK, `{"id":"org-1","name":"Org","timezone":"UTC"}`), nil
		case r.Method == http.MethodPost && r.URL.Path == "/org/org-1/rules/execute":
			return jsonResponse(r, executeStatus, executeBody), nil
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		return nil, nil
	})
}

// Bulk Run refused with 403 feature_not_enabled says the organization lacks
// parallel rule processing and names rule run as the way out.
func TestRuleBulkRunExplainsMissingParallelRuleProcessing(t *testing.T) {
	runtime := bulkRunRuntime(t, http.StatusForbidden, `{"code":"feature_not_enabled","message":"Parallel rule processing is not enabled for this organization"}`)
	output, err := invokeRuleCommand(t, runtime, newBulkRunRulesCmd(), []string{"--yes", "--json", "--from-date", "2026-01-01", "--to-date", "2026-01-31"})
	if err == nil {
		t.Fatalf("expected a refusal, output: %s", output)
	}
	for _, want := range []string{"parallel rule processing is not enabled", "rule run"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err.Error(), want)
		}
	}
	var result mutationEnvelope
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode %q: %v", output, err)
	}
	if result.Error == nil || result.Error.Code != "api_error" || result.Error.HTTPStatus != http.StatusForbidden || !strings.Contains(result.Error.Message, "parallel rule processing is not enabled") {
		t.Fatalf("envelope = %s", output)
	}
}

// Other refusals keep the server's words; a 202 is still a start.
func TestRuleBulkRunKeepsOtherOutcomes(t *testing.T) {
	runtime := bulkRunRuntime(t, http.StatusForbidden, `{"code":"security_error","message":"User lacks rules:update"}`)
	_, err := invokeRuleCommand(t, runtime, newBulkRunRulesCmd(), []string{"--yes", "--json", "--from-date", "2026-01-01", "--to-date", "2026-01-31"})
	if err == nil || !strings.Contains(err.Error(), "User lacks rules:update") || strings.Contains(err.Error(), "parallel rule processing") {
		t.Fatalf("err = %v", err)
	}
	runtime = bulkRunRuntime(t, http.StatusAccepted, `{"accepted":true}`)
	output, err := invokeRuleCommand(t, runtime, newBulkRunRulesCmd(), []string{"--yes", "--json", "--from-date", "2026-01-01", "--to-date", "2026-01-31"})
	if err != nil {
		t.Fatalf("%v (output: %s)", err, output)
	}
	var result mutationEnvelope
	if err := json.Unmarshal([]byte(output), &result); err != nil || result.Status != "success" {
		t.Fatalf("envelope = %s err=%v", output, err)
	}
}

// A bulk run with no --from-date is refused before anything is sent: the old
// default (2000-01-01) made api-svc enqueue one unit of work per hour of the
// organization's whole history in one request.
func TestRuleBulkRunRefusesWithoutFromDate(t *testing.T) {
	for _, flags := range [][]string{
		{"--yes", "--json"},
		{"--yes", "--json", "--from-date", "  "},
		{"--yes", "--json", "--to-date", "2026-01-31"},
	} {
		runtime := ruleRunRuntime(t, func(r *http.Request) (*http.Response, error) {
			t.Errorf("nothing may be sent for a dateless bulk run: %s %s", r.Method, r.URL.String())
			return nil, errors.New("unexpected request")
		})
		output, err := invokeRuleCommand(t, runtime, newBulkRunRulesCmd(), flags)
		var validation *op.ValidationError
		if !errors.As(err, &validation) || !strings.Contains(err.Error(), "--from-date is required") || !strings.Contains(err.Error(), "rule run") {
			t.Fatalf("flags %v: err = %v, want a from-date validation error naming rule run", flags, err)
		}
		var result mutationEnvelope
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("decode %q: %v", output, err)
		}
		if result.Status == "success" || result.Error == nil || !strings.Contains(result.Error.Message, "--from-date is required") {
			t.Fatalf("envelope = %s", output)
		}
	}
}

// The dry run also needs the range, so a preview never shows an unbounded
// request.
func TestRuleBulkRunDryRunNeedsFromDate(t *testing.T) {
	runtime := bulkRunRuntime(t, http.StatusAccepted, `{"accepted":true}`)
	if _, err := invokeRuleCommand(t, runtime, newBulkRunRulesCmd(), []string{"--dry-run", "--json"}); err == nil || !strings.Contains(err.Error(), "--from-date is required") {
		t.Fatalf("err = %v", err)
	}
	output, err := invokeRuleCommand(t, runtime, newBulkRunRulesCmd(), []string{"--dry-run", "--json", "--from-date", "2026-01-01", "--to-date", "2026-01-31"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, `"status": "preview"`) {
		t.Fatalf("output = %s", output)
	}
}

func TestRuleBulkRunHelpSaysFromDateIsRequired(t *testing.T) {
	cmd := newBulkRunRulesCmd()
	for _, want := range []string{"--from-date is required", "one unit of work per hour", "parallel rule processing", "rule run"} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("bulk-run help does not say %q", want)
		}
	}
	if usage := cmd.Flags().Lookup("from-date").Usage; !strings.Contains(usage, "required") || strings.Contains(usage, "2000-01-01") {
		t.Errorf("from-date usage = %q", usage)
	}
	if strings.Contains(cmd.Long, "\u2014") {
		t.Error("bulk-run help contains an em dash")
	}
}
