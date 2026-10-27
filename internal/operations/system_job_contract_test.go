package operations

import (
	"strings"
	"testing"
)

// Every operation that submits runSystemJob must be unable to start a contracted job without that job's
// contract being enforced, whatever the operation is called.
func TestNoSystemJobOperationCanStartAContractedJobWithoutItsContract(t *testing.T) {
	// A rollup selected by id with no walletId would be widened to every wallet by the backend.
	rollupWithoutWallet := map[string]any{"systemJobId": rollupSystemJobID, "startSEC": "2026-01-01", "endSEC": "2026-10-15"}
	// Reprice with the plural wallet field and reversed dates, which the reprice contract rejects.
	badReprice := map[string]any{"systemJobId": "reprice-failed-to-price-transactions", "walletIds": []any{"w1"}, "startSEC": "2026-10-15", "endSEC": "2026-01-01"}

	swept := 0
	for _, operation := range adminOperations() {
		if operation.Document != gqlSystemJobRun {
			continue
		}
		swept++
		for name, input := range map[string]map[string]any{"rollup without wallet": rollupWithoutWallet, "bad reprice": badReprice} {
			if err := rejectJobOverride(operation, input); err != nil {
				continue // refused because the operation's job is fixed
			}
			payload, candidate := adminPayloadFor(operation, input)
			if payload == nil {
				t.Errorf("%s: %s would be submitted with no contract at all", operation.Name, name)
				continue
			}
			if err := payload.Validate(candidate); err == nil {
				t.Errorf("%s: %s passed validation", operation.Name, name)
			}
		}
	}
	if swept < 10 {
		t.Fatalf("only %d system-job operations were swept; the sweep is not finding them", swept)
	}
}

func TestFixedJobOperationsRefuseToChangeTheirJob(t *testing.T) {
	var ignore adminOperation
	for _, operation := range adminOperations() {
		if operation.Name == "system-job-ignore" {
			ignore = operation
		}
	}
	for _, input := range []map[string]any{
		{"systemJobId": rollupSystemJobID},
		{"action": "reprocessRollupParentTxns"},
	} {
		if err := rejectJobOverride(ignore, input); err == nil {
			t.Errorf("input %v changed the job an ignore operation runs", input)
		}
	}
	if err := rejectJobOverride(ignore, map[string]any{"systemJobId": ignore.Defaults["systemJobId"], "action": ignore.Defaults["action"]}); err != nil {
		t.Errorf("repeating the operation's own job is harmless and must be allowed: %v", err)
	}
}

func TestGenericRunChoosesItsContractFromTheJobItSends(t *testing.T) {
	var run adminOperation
	for _, operation := range adminOperations() {
		if operation.Name == "system-job-run" {
			run = operation
		}
	}
	if payload, _ := adminPayloadFor(run, map[string]any{"systemJobId": rollupSystemJobID}); payload == nil {
		t.Error("run pointed at the rollup job must use the rollup contract")
	}
	if payload, _ := adminPayloadFor(run, map[string]any{"systemJobId": "some-other-job"}); payload != nil {
		t.Error("a job without a contract must not be given one")
	}
	if err := rejectJobOverride(run, map[string]any{"systemJobId": rollupSystemJobID}); err != nil {
		t.Errorf("run is the operation that chooses its job: %v", err)
	}
}

func TestEveryKnownSystemJobHasAContractAndEveryFixedJobIsKnown(t *testing.T) {
	for _, id := range knownSystemJobIDs {
		if systemJobContract(id) == nil {
			t.Errorf("%s has no contract", id)
		}
	}
	for _, operation := range adminOperations() {
		if operation.Document != gqlSystemJobRun {
			continue
		}
		id, _ := operation.Defaults["systemJobId"].(string)
		if id == "" {
			if operation.Name != "system-job-run" {
				t.Errorf("%s runs a system job without fixing one", operation.Name)
			}
			continue
		}
		if systemJobContract(id) == nil {
			t.Errorf("%s fixes unknown job %s", operation.Name, id)
		}
		if action, ok := operation.Defaults["action"].(string); ok && id == bulkTransactionSystemJobID {
			if err := bulkTransactionPayload().Validate(map[string]any{"action": action}); err != nil {
				t.Errorf("%s fixes an action its own contract rejects: %v", operation.Name, err)
			}
		}
	}
}

func TestSystemJobHelpStatesWhatEachCommandDoesAndItsDateContract(t *testing.T) {
	want := map[string][]string{
		"system-job-mark-reconciled":      {"Mark transactions as synced (reconciled) without sending them to the ERP", "YYYY-MM-DD", "omit for all wallets", "inclusive", "allowCategorizedModification  ignore only"},
		"system-job-mark-unreconciled":    {"Undo mark-synced", "YYYY-MM-DD"},
		"system-job-reconcile":            {"Sync (send) categorized transactions to the connected ERP", "real sync", "YYYY-MM-DD"},
		"system-job-run":                  {"systemJobId is required", "bulk-transaction", "delete-wallet", "csvBulkAction", "markUnreconciled", "YYYY-MM-DD", "org admin system-jobs mark-reconciled"},
		"system-job-reprice-failed":       {"YYYY-MM-DD", "omit for all wallets"},
		"system-job-process-needs-review": {"accept", "closed-needs-review", "YYYY-MM-DD"},
		"system-job-reprocess-rollups":    {"YYYY-MM-DD", "one wallet"},
	}
	found := 0
	for _, operation := range adminOperations() {
		texts, ok := want[operation.Name]
		if !ok {
			continue
		}
		found++
		help := operation.Short + "\n" + operation.Long
		for _, text := range texts {
			if !strings.Contains(help, text) {
				t.Errorf("%s help is missing %q", operation.Name, text)
			}
		}
	}
	if found != len(want) {
		t.Fatalf("found %d of %d operations", found, len(want))
	}
}
