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

// adminPayloadFor returns the contract that applies to a request, including the
// generic `run` operation when it is pointed at a job that has a contract.
func adminPayloadFor(operation adminOperation, input map[string]any) (*op.Payload, map[string]any) {
	if operation.Payload != nil {
		return operation.Payload, input
	}
	if operation.Name == "system-job-run" && input["systemJobId"] == rollupSystemJobID {
		stripped := make(map[string]any, len(input))
		for key, value := range input {
			if key != "systemJobId" && key != "action" {
				stripped[key] = value
			}
		}
		return rollupReprocessPayload(), stripped
	}
	return nil, input
}
