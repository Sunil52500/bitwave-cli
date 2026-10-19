package operations

import (
	op "github.com/bitwave-io/bitwave-cli/internal/operation"
)

const rollupSystemJobID = "reprocess-rollup-parent-txns"

// rollupReprocessPayload is the one contract for starting rollup reprocessing,
// shared by the CLI and by Wavie's hosted tool schema. Wallet scope is required
// here even though the backend would otherwise widen an omitted wallet to every
// wallet in the organization. Existing rollup rules are never part of the
// request, so they cannot be changed by it.
func rollupReprocessPayload() *op.Payload {
	return &op.Payload{
		Description: "Reprocess rollup parents for exactly one wallet using the organization's existing rollup rules. Dates are calendar days in the organization's timezone: the run covers the start of startSEC through the end of endSEC, inclusive. Despite their names, startSEC and endSEC take YYYY-MM-DD strings, not Unix seconds.",
		Fields: []op.PayloadField{
			{Name: "walletId", Type: op.PayloadString, Required: true, Description: "ID of the single wallet to reprocess. Run once per wallet; walletIds is not supported.", Hint: "one wallet ID string; to reprocess several wallets start one job per wallet"},
			{Name: "startSEC", Type: op.PayloadDate, Required: true, Description: "First day to reprocess, YYYY-MM-DD in the organization's timezone.", Hint: "for example 2026-04-01"},
			{Name: "endSEC", Type: op.PayloadDate, Required: true, Description: "Last day to reprocess, YYYY-MM-DD in the organization's timezone, inclusive.", Hint: "for example 2026-10-15"},
			{Name: "processNewTxns", Type: op.PayloadBoolean, Description: "Also roll up transactions not yet processed. Defaults to true."},
			{Name: "updateChildrenForExistingParent", Type: op.PayloadBoolean, Description: "Re-evaluate children of parents that already exist. Defaults to false."},
		},
		Check: startNotAfterEnd,
	}
}

// startNotAfterEnd enforces date ordering when both dates are present. Dates are
// already known to be well-formed YYYY-MM-DD, so string order is date order.
func startNotAfterEnd(values map[string]any) string {
	start, startOK := values["startSEC"].(string)
	end, endOK := values["endSEC"].(string)
	if startOK && endOK && start > end {
		return "startSEC must not be after endSEC"
	}
	return ""
}

// repricePayload covers the reprice jobs. Every field is optional because the
// backend treats an omitted wallet or date as unbounded.
func repricePayload() *op.Payload {
	return &op.Payload{
		Description: "Reprice transactions, optionally limited to one wallet and a date range. Dates are calendar days in the organization's timezone, inclusive. Despite their names, startSEC and endSEC take YYYY-MM-DD strings, not Unix seconds.",
		Fields: []op.PayloadField{
			{Name: "walletId", Type: op.PayloadString, Description: "ID of one wallet to limit the job to. Omit for all wallets."},
			{Name: "startSEC", Type: op.PayloadDate, Description: "First day to reprice, YYYY-MM-DD.", Hint: "for example 2026-04-01"},
			{Name: "endSEC", Type: op.PayloadDate, Description: "Last day to reprice, YYYY-MM-DD, inclusive.", Hint: "for example 2026-10-15"},
		},
		Check: startNotAfterEnd,
	}
}

// systemJobContract returns the payload contract for a system job, or nil when the job has none yet.
func systemJobContract(systemJobID string) *op.Payload {
	switch systemJobID {
	case rollupSystemJobID:
		return rollupReprocessPayload()
	case "reprice-failed-to-price-transactions", "reprice-priced-and-ready-to-price-transactions":
		return repricePayload()
	}
	return nil
}

// adminPayloadFor returns the contract that applies to a request. For system-job operations it is chosen by the
// job that will actually run (the operation's fixed job, or the systemJobId sent to the generic `run`), never by
// the operation's name, so no operation can be used to start a contracted job without its contract.
func adminPayloadFor(operation adminOperation, input map[string]any) (*op.Payload, map[string]any) {
	if operation.Document != gqlSystemJobRun {
		return operation.Payload, input
	}
	jobID, _ := operation.Defaults["systemJobId"].(string)
	if jobID == "" {
		jobID, _ = input["systemJobId"].(string)
	}
	payload := systemJobContract(jobID)
	if payload == nil {
		return nil, input
	}
	stripped := make(map[string]any, len(input))
	for key, value := range input {
		if key != "systemJobId" && key != "action" {
			stripped[key] = value
		}
	}
	return payload, stripped
}

// rejectJobOverride stops --data from changing which job a fixed-job operation runs. `run` is the only
// operation that chooses its job.
func rejectJobOverride(operation adminOperation, input map[string]any) error {
	if operation.Document != gqlSystemJobRun || operation.Defaults["systemJobId"] == nil {
		return nil
	}
	if value, sent := input["systemJobId"]; sent && value != operation.Defaults["systemJobId"] {
		return op.NewValidationError("systemJobId is fixed for this operation and cannot be changed in the payload; use the system-jobs run operation to choose a job. Nothing was submitted.")
	}
	if value, sent := input["action"]; sent && value != operation.Defaults["action"] {
		return op.NewValidationError("action is fixed for this operation and cannot be changed in the payload; use the system-jobs run operation to choose a job. Nothing was submitted.")
	}
	return nil
}
