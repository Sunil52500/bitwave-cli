package orgreports

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bitwave-io/bitwave-cli/internal/apierr"
)

func TestRuleContracts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/graphql-reports":
			var request struct {
				OperationName string `json:"operationName"`
				Variables     struct {
					OrgID string `json:"orgId"`
				} `json:"variables"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.OperationName != "rules" || request.Variables.OrgID != "org-1" {
				t.Fatalf("request = %#v", request)
			}
			_, _ = w.Write([]byte(`{"data":{"rules":[{"id":"rule-1","name":"ETH inflows","disabled":true,"coin":"ETH"}]}}`))
		case "/graphql":
			var request struct {
				OperationName string `json:"operationName"`
				Variables     struct {
					OrgID string         `json:"orgId"`
					Rule  map[string]any `json:"rule"`
				} `json:"variables"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.OperationName != "CreateRule" || request.Variables.OrgID != "org-1" || request.Variables.Rule["transfer"] == nil {
				t.Fatalf("request = %#v", request)
			}
			_, _ = w.Write([]byte(`{"data":{"createRule":{"success":true,"errors":[]}}}`))
		case "/orgs/org-1/transactions/txn-1/rules/rule-1":
			if r.Method != http.MethodGet {
				t.Fatalf("method = %s", r.Method)
			}
			_, _ = w.Write([]byte(`{"valid":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(server.URL, func() (string, error) { return "token", nil })
	ctx := context.Background()
	rules, err := client.Rules(ctx, "org-1")
	if err != nil || len(rules) != 1 {
		t.Fatalf("rules = %#v err=%v", rules, err)
	}
	created, err := client.CreateRule(ctx, "org-1", json.RawMessage(`{"transfer":{"name":"ETH inflows"}}`))
	if err != nil || !created.Success {
		t.Fatalf("created = %#v err=%v", created, err)
	}
	validation, err := client.ValidateRule(ctx, "org-1", "txn-1", "rule-1")
	if err != nil || string(validation) != `{"valid":true}` {
		t.Fatalf("validation = %s err=%v", validation, err)
	}
}

func TestRuleReadAndMutationContracts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		var request struct {
			OperationName string         `json:"operationName"`
			Variables     map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		switch request.OperationName {
		case "rule":
			_, _ = w.Write([]byte(`{"data":{"rule":{"id":"rule-1","name":"one","disabled":false,"priority":1,"direction":"Inbound","action":{"type":"Ignore","__typename":"IgnoreAction"},"__typename":"TransferRule"}}}`))
		case "rulesPaginated":
			_, _ = w.Write([]byte(`{"data":{"rulesPaginated":{"items":[{"id":"rule-1","name":"one","disabled":false,"priority":1,"direction":"Inbound","action":{"type":"Ignore","__typename":"IgnoreAction"},"__typename":"TransferRule"}],"nextPageToken":"next"}}}`))
		case "ToggleRuleStatus":
			if request.Variables["disabled"] != true {
				t.Fatalf("variables = %#v", request.Variables)
			}
			_, _ = w.Write([]byte(`{"data":{"toggleRuleStatus":true}}`))
		case "DeleteRule":
			_, _ = w.Write([]byte(`{"data":{"deleteRule":true}}`))
		default:
			t.Fatalf("operation = %q", request.OperationName)
		}
	}))
	defer server.Close()

	client := New(server.URL, func() (string, error) { return "token", nil })
	ctx := context.Background()
	rule, err := client.Rule(ctx, "org-1", "rule-1")
	if err != nil || !json.Valid(rule) {
		t.Fatalf("rule = %s err=%v", rule, err)
	}
	page, err := client.RulesPage(ctx, "org-1", 25, "")
	if err != nil || len(page.Items) != 1 || page.NextPageToken != "next" {
		t.Fatalf("page = %#v err=%v", page, err)
	}
	if err := client.ToggleRule(ctx, "org-1", "rule-1", true); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteRule(ctx, "org-1", "rule-1"); err != nil {
		t.Fatal(err)
	}
}

func TestProductionRuleEndpoints(t *testing.T) {
	client := New("https://api.bitwave.io", func() (string, error) { return "", nil })
	if client.RulesQueryURL != "https://api4.bitwave.io/graphql-reports" {
		t.Fatalf("query URL = %s", client.RulesQueryURL)
	}
	if client.RulesMutationURL != "https://api-app.bitwave.io/graphql" {
		t.Fatalf("mutation URL = %s", client.RulesMutationURL)
	}
}

func TestUpdateRunAndBulkRuleContracts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/org/org-1/rules/execute" {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["executeUpdates"] != "true" || body["after"] != float64(100) || body["before"] != float64(200) {
				t.Fatalf("bulk body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"accepted":true}`))
			return
		}
		var request struct {
			OperationName string         `json:"operationName"`
			Variables     map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		switch request.OperationName {
		case "UpdateRule":
			if request.Variables["ruleId"] != "rule-1" {
				t.Fatalf("variables = %#v", request.Variables)
			}
			_, _ = w.Write([]byte(`{"data":{"updateRule":{"success":true,"errors":[]}}}`))
		default:
			t.Fatalf("operation = %q", request.OperationName)
		}
	}))
	defer server.Close()

	client := New(server.URL, func() (string, error) { return "token", nil })
	client.RulesMutationURL = server.URL
	ctx := context.Background()
	if _, err := client.UpdateRule(ctx, "org-1", "rule-1", json.RawMessage(`{"transfer":{"name":"updated"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := client.ExecuteBulkRules(ctx, "org-1", 100, 200); err != nil {
		t.Fatal(err)
	}
}

// The standard rules run goes to sync-coordinator's invoke route for the
// organization's deterministic rules-workflow job, with overrideHeartbeat=true,
// never to the GraphQL runRulesForOrg mutation.
func TestStartRulesRunInvokesTheSyncCoordinatorJob(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "graphql") {
			t.Fatalf("the rules run must not use GraphQL: %s %s", r.Method, r.URL.Path)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v3/orgs/org-1/jobs/api-svc-rules/rules-workflow/org-1.rules-workflow/invoke" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("overrideHeartbeat") != "true" {
			t.Fatalf("overrideHeartbeat must be sent: %q", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("missing bearer token: %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if strings.TrimSpace(string(body)) != "{}" {
			t.Fatalf("body = %q, want an empty JSON object", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jobRunId":"run-1","jobId":"org-1.rules-workflow","message":"Successfully invoked job org-1.rules-workflow"}`))
	}))
	defer server.Close()

	client := New(server.URL, func() (string, error) { return "token", nil })
	started, err := client.StartRulesRun(context.Background(), "org-1")
	if err != nil {
		t.Fatal(err)
	}
	if started.JobRunID != "run-1" || started.JobID != "org-1.rules-workflow" || !strings.Contains(started.Message, "invoked") {
		t.Fatalf("started = %#v", started)
	}
}

func TestRulesRunInvokePathEscapesTheOrganization(t *testing.T) {
	if got := RulesRunInvokePath("org 1/x"); got != "/v3/orgs/org%201%2Fx/jobs/api-svc-rules/rules-workflow/org%201%2Fx.rules-workflow/invoke?overrideHeartbeat=true" {
		t.Fatalf("path = %s", got)
	}
}

// A 2xx without a job run id is not a started run.
func TestStartRulesRunRejectsAnAnswerWithoutAJobRunID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"ok"}`))
	}))
	defer server.Close()
	client := New(server.URL, func() (string, error) { return "token", nil })
	if _, err := client.StartRulesRun(context.Background(), "org-1"); err == nil || !strings.Contains(err.Error(), "job run id") {
		t.Fatalf("err = %v, want a missing job run id error", err)
	}
}

// sync-coordinator answers 412 when the runner declines (already running,
// nothing to sync): the status and the server's reason reach the caller.
func TestStartRulesRunReportsADeclinedStart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = w.Write([]byte(`{"status":"FAILED_PRECONDITION","error":"job org-1.rules-workflow was not started: a run is already in progress"}`))
	}))
	defer server.Close()
	client := New(server.URL, func() (string, error) { return "token", nil })
	_, err := client.StartRulesRun(context.Background(), "org-1")
	var apiError *apierr.Error
	if !errors.As(err, &apiError) || apiError.Status != http.StatusPreconditionFailed || !strings.Contains(apiError.Detail, "was not started: a run is already in progress") {
		t.Fatalf("err = %v", err)
	}
}

// api-svc refuses Bulk Run with 403 feature_not_enabled for organizations
// without the parallel-rule-processing flag; the SDK says so in words that
// name the way out, with the status kept.
func TestExecuteBulkRulesExplainsMissingParallelRuleProcessing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/org/org-1/rules/execute" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"feature_not_enabled","message":"Parallel rule processing is not enabled for this organization"}`))
	}))
	defer server.Close()
	client := New(server.URL, func() (string, error) { return "token", nil })
	err := client.ExecuteBulkRules(context.Background(), "org-1", 100, 200)
	var apiError *apierr.Error
	if !errors.As(err, &apiError) {
		t.Fatalf("err = %v, want an API error", err)
	}
	if apiError.Status != http.StatusForbidden || apiError.Detail != BulkRulesNotEnabledDetail {
		t.Fatalf("err = %#v", apiError)
	}
	for _, want := range []string{"parallel rule processing is not enabled", "rule run"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err.Error(), want)
		}
	}
}

// Any other refusal (for example a 403 from the security check, or a 400)
// keeps the server's own words.
func TestExecuteBulkRulesKeepsOtherRefusalsVerbatim(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		detail string
	}{
		{http.StatusForbidden, `{"code":"security_error","message":"User lacks rules:update"}`, "User lacks rules:update"},
		{http.StatusBadRequest, `{"code":"invalid_date_range","message":"No valid time chunks found for the specified date range"}`, "No valid time chunks found for the specified date range"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		client := New(server.URL, func() (string, error) { return "token", nil })
		err := client.ExecuteBulkRules(context.Background(), "org-1", 100, 200)
		server.Close()
		var apiError *apierr.Error
		if !errors.As(err, &apiError) || apiError.Status != tc.status || apiError.Detail != tc.detail {
			t.Fatalf("status %d: err = %v", tc.status, err)
		}
	}
	if err := explainBulkRulesRefusal(nil); err != nil {
		t.Fatalf("nil must stay nil, got %v", err)
	}
	plain := errors.New("dial failed")
	if err := explainBulkRulesRefusal(plain); err != plain {
		t.Fatalf("a transport error must pass through unchanged, got %v", err)
	}
}
