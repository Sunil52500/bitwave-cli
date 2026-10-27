package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// offlineClient fails the test if anything reaches the network: every case here must be decided locally.
func offlineClient(t *testing.T) *Client {
	t.Helper()
	return NewClient(Options{OrganizationID: "org-1", Token: "test-token", HTTPClient: &http.Client{Transport: contractTransport(func(r *http.Request) (*http.Response, error) {
		t.Errorf("unexpected request to %s %s", r.Method, r.URL.Path)
		return nil, errors.New("network disabled in this test")
	})}})
}

func invokeOffline(t *testing.T, operation, arguments string) (Result, error) {
	t.Helper()
	return offlineClient(t).Invoke(context.Background(), Request{Operation: operation, Arguments: json.RawMessage(arguments)})
}

func adminArguments(t *testing.T, data string, mode string) string {
	t.Helper()
	return `{"data":` + string(contractJSON(t, data)) + `,"` + mode + `":true}`
}

func requireValidation(t *testing.T, err error, want ...string) *ValidationError {
	t.Helper()
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("err = %v (%T), want a ValidationError", err, err)
	}
	for _, text := range want {
		if !strings.Contains(validation.Error(), text) {
			t.Fatalf("message %q does not contain %q", validation.Error(), text)
		}
	}
	return validation
}

const (
	markReconciledTool = "bitwave_org_admin_system_jobs_mark_reconciled"
	needsReviewTool    = "bitwave_org_admin_system_jobs_process_needs_review"
	walletDeleteTool   = "bitwave_org_admin_wallets_delete"
)

// The payload an agent actually sent: no systemJobId, an invented action, and an epoch end date.
const inventedMarkSynced = `{"action":"mark-synced","endSEC":"1785542399"}`

func TestGenericRunRejectsTheInventedMarkSyncedPayloadInDryRunAndPointsToTheDedicatedCommand(t *testing.T) {
	for _, mode := range []string{"dry-run", "yes"} {
		t.Run(mode, func(t *testing.T) {
			_, err := invokeOffline(t, runTool, adminArguments(t, inventedMarkSynced, mode))
			requireValidation(t, err,
				"systemJobId is required",
				"bulk-transaction, delete-wallet, reprice-failed-to-price-transactions, reprice-priced-and-ready-to-price-transactions, process-needs-review-transactions, reprocess-rollup-parent-txns",
				"markReconciled",
				"org admin system-jobs mark-reconciled",
				"Nothing was submitted")
		})
	}
}

func TestGenericRunRejectsAnUnknownSystemJobWithoutEchoingIt(t *testing.T) {
	_, err := invokeOffline(t, runTool, adminArguments(t, `{"systemJobId":"mark-synced-SECRET","walletId":"w1"}`, "dry-run"))
	validation := requireValidation(t, err, "systemJobId is not a known system job", "org admin system-jobs mark-reconciled")
	if strings.Contains(validation.Error(), "SECRET") {
		t.Fatalf("message echoes submitted input: %q", validation.Error())
	}
	_, err = invokeOffline(t, runTool, adminArguments(t, `{"systemJobId":42}`, "dry-run"))
	requireValidation(t, err, "systemJobId is not a known system job")
}

func TestDryRunAndRealRunRejectAnInvalidPayloadWithTheSameMessage(t *testing.T) {
	for name, tc := range map[string]struct{ tool, data string }{
		"unknown job":         {runTool, `{"systemJobId":"mark-synced"}`},
		"invented action":     {runTool, `{"systemJobId":"bulk-transaction","action":"markSynced"}`},
		"epoch end date":      {markReconciledTool, `{"endSEC":"1785542399"}`},
		"reversed dates":      {markReconciledTool, `{"startSEC":"2026-03-31","endSEC":"2026-01-01"}`},
		"bad needs-review":    {needsReviewTool, `{"resolution":"approve"}`},
		"delete-wallet extra": {runTool, `{"systemJobId":"delete-wallet"}`},
	} {
		t.Run(name, func(t *testing.T) {
			_, dryErr := invokeOffline(t, tc.tool, adminArguments(t, tc.data, "dry-run"))
			_, realErr := invokeOffline(t, tc.tool, adminArguments(t, tc.data, "yes"))
			dry := requireValidation(t, dryErr)
			real := requireValidation(t, realErr)
			if dry.Error() != real.Error() {
				t.Fatalf("dry-run message %q differs from real message %q", dry.Error(), real.Error())
			}
		})
	}
}

func TestBulkTransactionContractThroughTheDedicatedCommandsAndRun(t *testing.T) {
	cases := []struct {
		name, tool, data, want string
	}{
		{"epoch start", markReconciledTool, `{"startSEC":"1775000000"}`, "startSEC must be a calendar date written YYYY-MM-DD, not Unix seconds"},
		{"numeric start", markReconciledTool, `{"startSEC":1775000000}`, "startSEC must be a calendar date string"},
		{"impossible date", "bitwave_org_admin_system_jobs_ignore", `{"endSEC":"2026-02-30"}`, "endSEC must be a real calendar date"},
		{"datetime", "bitwave_org_admin_system_jobs_unignore", `{"endSEC":"2026-02-01T00:00:00Z"}`, "endSEC must be a real calendar date"},
		{"reversed", "bitwave_org_admin_system_jobs_reconcile", `{"startSEC":"2026-03-31","endSEC":"2026-01-01"}`, "startSEC must not be after endSEC"},
		{"plural wallets", "bitwave_org_admin_system_jobs_uncategorize", `{"walletIds":["w1"]}`, "use walletId"},
		{"quoted boolean", "bitwave_org_admin_system_jobs_delete_transactions", `{"allowReconciledModification":"true"}`, "allowReconciledModification must be a JSON boolean"},
		{"matchCriteria not object", "bitwave_org_admin_system_jobs_ignore", `{"matchCriteria":"Inbound"}`, "matchCriteria must be a JSON object"},
		{"run missing action", runTool, `{"systemJobId":"bulk-transaction"}`, "action is required"},
		{"run invented action", runTool, `{"systemJobId":"bulk-transaction","action":"markSynced"}`, "action must be one of: delete, markReconciled, markUnreconciled, uncategorize, ignore, unignore, reconcile, csvBulkAction"},
		{"run deleteWallet via bulk", runTool, `{"systemJobId":"bulk-transaction","action":"deleteWallet","walletId":"w1"}`, "action must be one of"},
		{"csv without data source", runTool, `{"systemJobId":"bulk-transaction","action":"csvBulkAction"}`, "dataSourceId is required when action is csvBulkAction"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := invokeOffline(t, tc.tool, adminArguments(t, tc.data, "dry-run"))
			requireValidation(t, err, tc.want, "Nothing was submitted")
		})
	}
}

func TestValidBulkPayloadsPreviewWithoutContactingBitwave(t *testing.T) {
	for name, tc := range map[string]struct{ tool, data string }{
		"mark-reconciled all wallets": {markReconciledTool, `{}`},
		"mark-reconciled one wallet":  {markReconciledTool, `{"walletId":"w1","startSEC":"2026-01-01","endSEC":"2026-03-31","transactionType":"all","allowCategorizedModification":true}`},
		"same-day range":              {"bitwave_org_admin_system_jobs_ignore", `{"startSEC":"2026-01-01","endSEC":"2026-01-01","matchCriteria":{"direction":"Inbound"}}`},
		"repeat own action":           {markReconciledTool, `{"systemJobId":"bulk-transaction","action":"markReconciled"}`},
		"run bulk":                    {runTool, `{"systemJobId":"bulk-transaction","action":"markReconciled","endSEC":"2026-07-31"}`},
		"run csv":                     {runTool, `{"systemJobId":"bulk-transaction","action":"csvBulkAction","dataSourceId":"ds-1"}`},
		"run delete-wallet":           {runTool, `{"systemJobId":"delete-wallet","walletId":"w1"}`},
		"run needs-review":            {runTool, `{"systemJobId":"process-needs-review-transactions","resolution":"ignore","transactionState":"closed-needs-review"}`},
		"needs-review defaults":       {needsReviewTool, `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := invokeOffline(t, tc.tool, adminArguments(t, tc.data, "dry-run"))
			if err != nil {
				t.Fatalf("valid payload rejected: %v", err)
			}
			var out map[string]any
			if err := json.Unmarshal(result.Data, &out); err != nil {
				t.Fatal(err)
			}
			if out["status"] != "preview" {
				t.Fatalf("out = %v", out)
			}
		})
	}
}

func TestFixedBulkCommandsCannotBeRedirectedToAnotherActionOrJob(t *testing.T) {
	for _, data := range []string{
		`{"action":"reconcile"}`,
		`{"action":"mark-synced"}`,
		`{"systemJobId":"delete-wallet","walletId":"w1"}`,
	} {
		_, err := invokeOffline(t, markReconciledTool, adminArguments(t, data, "dry-run"))
		requireValidation(t, err, "is fixed for this operation")
	}
}

func TestProcessNeedsReviewAndDeleteWalletContracts(t *testing.T) {
	cases := []struct{ name, tool, data, want string }{
		{"bad resolution", needsReviewTool, `{"resolution":"approve"}`, "resolution must be one of: accept, ignore"},
		{"bad state", needsReviewTool, `{"transactionState":"needs-review"}`, "transactionState must be one of: open-needs-review, new-needs-review, closed-needs-review"},
		{"epoch date", needsReviewTool, `{"endSEC":"1785542399"}`, "not Unix seconds"},
		{"run needs-review missing fields", runTool, `{"systemJobId":"process-needs-review-transactions"}`, "resolution is required"},
		{"run delete-wallet missing wallet", runTool, `{"systemJobId":"delete-wallet"}`, "walletId is required"},
		{"run delete-wallet extra field", runTool, `{"systemJobId":"delete-wallet","walletId":"w1","startSEC":"2026-01-01"}`, `"startSEC" is not a supported field`},
		{"wallets delete extra field", walletDeleteTool, `{"walletIds":["w2"]}`, `"walletIds" is not a supported field`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			arguments := adminArguments(t, tc.data, "dry-run")
			if tc.tool == walletDeleteTool {
				arguments = `{"arguments":["w1"],"data":` + string(contractJSON(t, tc.data)) + `,"dry-run":true}`
			}
			_, err := invokeOffline(t, tc.tool, arguments)
			requireValidation(t, err, tc.want)
		})
	}
	if _, err := invokeOffline(t, walletDeleteTool, `{"arguments":["w1"],"dry-run":true}`); err != nil {
		t.Fatalf("wallets delete takes its wallet from the argument: %v", err)
	}
}

func TestRepriceAndRollupContractsRejectEpochDates(t *testing.T) {
	for _, tc := range []struct{ tool, data string }{
		{repriceTool, `{"startSEC":"1775000000"}`},
		{"bitwave_org_admin_system_jobs_reprice_ready", `{"endSEC":"1785542399"}`},
		{rollupTool, `{"walletId":"w1","startSEC":"2026-04-01","endSEC":"1785542399"}`},
		{runTool, `{"systemJobId":"reprocess-rollup-parent-txns","walletId":"w1","startSEC":"1775000000","endSEC":"2026-10-15"}`},
	} {
		_, err := invokeOffline(t, tc.tool, adminArguments(t, tc.data, "dry-run"))
		requireValidation(t, err, "not Unix seconds")
	}
}

func TestFixedBulkCommandsPublishTheirContractWithoutTheFixedAction(t *testing.T) {
	list, err := Operations()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Operation{}
	for _, operation := range list {
		byName[operation.Name] = operation
	}
	schema := byName[markReconciledTool].PayloadSchema
	if schema == nil {
		t.Fatal("mark-reconciled must publish the bulk-transaction contract")
	}
	properties := schema["properties"].(map[string]any)
	if _, ok := properties["action"]; ok {
		t.Fatal("the fixed action must not be advertised as a payload field")
	}
	if properties["matchCriteria"].(map[string]any)["type"] != "object" || properties["startSEC"].(map[string]any)["format"] != "date" {
		t.Fatalf("properties = %v", properties)
	}
	if _, ok := schema["required"]; ok {
		t.Fatalf("every bulk field is optional: %v", schema["required"])
	}
	review := byName[needsReviewTool].PayloadSchema
	if _, ok := review["required"]; ok {
		t.Fatalf("process-needs-review defaults resolution and transactionState: %v", review["required"])
	}
	if !strings.Contains(byName[markReconciledTool].Description, "without sending them to the ERP") {
		t.Fatalf("description = %q", byName[markReconciledTool].Description)
	}
	run := byName[runTool].Description
	for _, want := range []string{"bulk-transaction", "delete-wallet", "process-needs-review-transactions", "reprocess-rollup-parent-txns", "markReconciled", "csvBulkAction", "YYYY-MM-DD", "org admin system-jobs mark-reconciled"} {
		if !strings.Contains(run, want) {
			t.Fatalf("run description is missing %q", want)
		}
	}
}

func TestTransactionReadInputErrorsAreValidationErrors(t *testing.T) {
	cases := []struct{ name, tool, arguments, want string }{
		{"count from without to", "bitwave_transaction_count", `{"from":"2026-01-01"}`, "--from and --to must be supplied together"},
		{"count too many searches", "bitwave_transaction_count", `{"search":["a","b","c","d","e","f"]}`, "--search may be supplied at most five times"},
		{"count bad amount", "bitwave_transaction_count", `{"amount-min":"lots"}`, "--amount-min must be a number"},
		{"count bad date", "bitwave_transaction_count", `{"from":"2026-02-30","to":"2026-03-01"}`, "--from must be a valid calendar date"},
		{"count reversed", "bitwave_transaction_count", `{"from":"2026-03-01","to":"2026-02-01"}`, "--from must be on or before --to"},
		{"review summary from without to", "bitwave_transaction_review_summary", `{"to":"2026-01-01"}`, "--from and --to must be supplied together"},
		{"review analyze limit", "bitwave_transaction_review_analyze", `{"limit":0}`, "--limit must be between 1 and 100"},
		{"categorization options limit", "bitwave_transaction_categorization_options", `{"limit":501}`, "--limit must be between 1 and 500"},
		{"export bad date", "bitwave_report_transaction_export", `{"from":"2026-13-01","to":"2026-12-31"}`, "--from must be a valid calendar date"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := invokeOffline(t, tc.tool, tc.arguments)
			requireValidation(t, err, tc.want)
		})
	}
}
