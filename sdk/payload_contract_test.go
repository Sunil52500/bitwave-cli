package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

type payloadHarness struct {
	t     *testing.T
	calls int
	body  string
}

func (h *payloadHarness) client() *Client {
	return NewClient(Options{OrganizationID: "org-1", Token: "test-token", HTTPClient: &http.Client{Transport: contractTransport(func(r *http.Request) (*http.Response, error) {
		h.calls++
		return contractResponse(r, h.body), nil
	})}})
}

func (h *payloadHarness) invoke(operation string, data string, extra string) (Result, error) {
	h.t.Helper()
	arguments := `{"data":` + string(contractJSON(h.t, data)) + extra + `}`
	return h.client().Invoke(context.Background(), Request{Operation: operation, Arguments: json.RawMessage(arguments)})
}

const (
	rollupTool  = "bitwave_org_admin_system_jobs_reprocess_rollups"
	repriceTool = "bitwave_org_admin_system_jobs_reprice_failed"
	runTool     = "bitwave_org_admin_system_jobs_run"
	goodRollup  = `{"walletId":"w1","startSEC":"2026-04-01","endSEC":"2026-10-15","processNewTxns":true,"updateChildrenForExistingParent":false}`
)

func TestRollupContractRejectsInvalidPayloadsBeforeAnyRequest(t *testing.T) {
	cases := map[string]struct{ data, want string }{
		"plural wallets": {`{"walletIds":["w1"],"startSEC":"2026-04-01","endSEC":"2026-10-15"}`, "use walletId"},
		"missing wallet": {`{"startSEC":"2026-04-01","endSEC":"2026-10-15"}`, "walletId is required"},
		"quoted boolean": {`{"walletId":"w1","startSEC":"2026-04-01","endSEC":"2026-10-15","processNewTxns":"true"}`, "processNewTxns must be a JSON boolean"},
		"unix seconds":   {`{"walletId":"w1","startSEC":1775000000,"endSEC":"2026-10-15"}`, "startSEC must be a calendar date string"},
		"reversed dates": {`{"walletId":"w1","startSEC":"2026-10-15","endSEC":"2026-04-01"}`, "startSEC must not be after endSEC"},
		"unknown field":  {`{"walletId":"w1","startSEC":"2026-04-01","endSEC":"2026-10-15","rules":[]}`, `"rules" is not a supported field`},
		"empty object":   {`{}`, "walletId is required"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := &payloadHarness{t: t}
			_, err := h.invoke(rollupTool, tc.data, `,"yes":true`)
			var validation *ValidationError
			if !errors.As(err, &validation) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want ValidationError containing %q", err, tc.want)
			}
			if h.calls != 0 {
				t.Fatalf("an invalid payload reached the network (%d calls)", h.calls)
			}
		})
	}
}

func TestRollupContractRejectsInvalidPayloadsEvenForDryRun(t *testing.T) {
	h := &payloadHarness{t: t}
	if _, err := h.invoke(rollupTool, `{"walletIds":["w1"]}`, `,"dry-run":true`); err == nil {
		t.Fatal("dry run must validate too, so a preview never looks valid when the real call would fail")
	}
}

func TestAcceptedRollupJobReturnsJobIDAndDoesNotClaimProgress(t *testing.T) {
	h := &payloadHarness{t: t, body: `{"data":{"runSystemJob":"job-123"}}`}
	result, err := h.invoke(rollupTool, goodRollup, `,"yes":true`)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(result.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out["outcome"] != "accepted" || out["jobRunId"] != "job-123" {
		t.Fatalf("out = %v", out)
	}
	if !strings.Contains(out["note"].(string), "does not mean the job has progressed") {
		t.Fatalf("acceptance must not be reported as completion: %v", out["note"])
	}
	if h.calls != 1 {
		t.Fatalf("calls = %d", h.calls)
	}
}

func TestDryRunIsLabelledAsAPreviewThatSubmitsNothing(t *testing.T) {
	h := &payloadHarness{t: t}
	result, err := h.invoke(rollupTool, goodRollup, `,"dry-run":true`)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(result.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "preview" || out["submitted"] != false || !strings.Contains(out["note"].(string), "Nothing was submitted") {
		t.Fatalf("out = %v", out)
	}
	if h.calls != 0 || out["jobRunId"] != nil {
		t.Fatalf("a preview must not contact Bitwave or return a job ID (calls=%d out=%v)", h.calls, out)
	}
}

func TestGenericRunAppliesTheRollupContractButLeavesOtherJobsAlone(t *testing.T) {
	h := &payloadHarness{t: t, body: `{"data":{"runSystemJob":"job-9"}}`}
	if _, err := h.invoke(runTool, `{"systemJobId":"reprocess-rollup-parent-txns","walletIds":["w1"],"startSEC":"2026-04-01","endSEC":"2026-10-15"}`, `,"yes":true`); err == nil || h.calls != 0 {
		t.Fatalf("generic run bypassed the rollup contract (err=%v calls=%d)", err, h.calls)
	}
	if _, err := h.invoke(runTool, `{"systemJobId":"reprocess-rollup-parent-txns","action":"reprocessRollupParentTxns","walletId":"w1","startSEC":"2026-04-01","endSEC":"2026-10-15"}`, `,"yes":true`); err != nil {
		t.Fatalf("valid rollup via generic run rejected: %v", err)
	}
	if _, err := h.invoke(runTool, `{"systemJobId":"bulk-transaction","action":"ignore","transactionType":"all"}`, `,"yes":true`); err != nil {
		t.Fatalf("unrelated job must not be affected by the rollup contract: %v", err)
	}
}

// Representative non-rollup action: the same mechanism guards the reprice jobs.
func TestRepriceJobsUseTheSameSharedMechanism(t *testing.T) {
	h := &payloadHarness{t: t, body: `{"data":{"runSystemJob":"job-7"}}`}
	for name, data := range map[string]string{
		"impossible date": `{"walletId":"w1","startSEC":"2026-02-30"}`,
		"reversed dates":  `{"startSEC":"2026-10-15","endSEC":"2026-04-01"}`,
		"plural wallets":  `{"walletIds":["w1"]}`,
	} {
		before := h.calls
		if _, err := h.invoke(repriceTool, data, `,"yes":true`); err == nil || h.calls != before {
			t.Fatalf("%s: err=%v calls=%d", name, err, h.calls-before)
		}
	}
	if _, err := h.invoke(repriceTool, `{"walletId":"w1","startSEC":"2026-04-01","endSEC":"2026-04-30"}`, `,"yes":true`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.invoke(repriceTool, `{}`, `,"yes":true`); err != nil {
		t.Fatalf("every reprice field is optional: %v", err)
	}
}

func TestDescriptorsPublishThePayloadContractHostedToolsUse(t *testing.T) {
	list, err := Operations()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Operation{}
	for _, operation := range list {
		byName[operation.Name] = operation
	}
	rollup := byName[rollupTool].PayloadSchema
	if rollup == nil || rollup["additionalProperties"] != false {
		t.Fatalf("rollup payload schema = %v", rollup)
	}
	if got := rollup["required"].([]string); strings.Join(got, ",") != "endSEC,startSEC,walletId" {
		t.Fatalf("required = %v", got)
	}
	properties := rollup["properties"].(map[string]any)
	if _, ok := properties["walletIds"]; ok {
		t.Fatal("walletIds must not be advertised")
	}
	if properties["processNewTxns"].(map[string]any)["type"] != "boolean" {
		t.Fatalf("processNewTxns = %v", properties["processNewTxns"])
	}
	if byName[repriceTool].PayloadSchema == nil {
		t.Fatal("reprice tool must publish a contract through the same mechanism")
	}
	if byName["bitwave_org_admin_system_jobs_ignore"].PayloadSchema != nil {
		t.Fatal("operations without a contract must not advertise one")
	}
}
