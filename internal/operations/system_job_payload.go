package operations

import (
	"strings"

	op "github.com/bitwave-io/bitwave-cli/internal/operation"
)

// System job identifiers accepted by the Bitwave platform's runSystemJob mutation.
const (
	bulkTransactionSystemJobID    = "bulk-transaction"
	deleteWalletSystemJobID       = "delete-wallet"
	repriceFailedSystemJobID      = "reprice-failed-to-price-transactions"
	repriceReadySystemJobID       = "reprice-priced-and-ready-to-price-transactions"
	processNeedsReviewSystemJobID = "process-needs-review-transactions"
)

// knownSystemJobIDs lists every job runSystemJob accepts, in the order help text shows them.
var knownSystemJobIDs = []string{
	bulkTransactionSystemJobID,
	deleteWalletSystemJobID,
	repriceFailedSystemJobID,
	repriceReadySystemJobID,
	processNeedsReviewSystemJobID,
	rollupSystemJobID,
}

// bulkTransactionActions are the bulk-transaction actions the platform implements. deleteWallet also exists
// server side but is run through the delete-wallet job, not through bulk-transaction.
var bulkTransactionActions = []string{
	"delete", "markReconciled", "markUnreconciled", "uncategorize", "ignore", "unignore", "reconcile", "csvBulkAction",
}

const systemJobDateRule = "Dates are calendar days written YYYY-MM-DD in the organization's timezone, inclusive of both whole days. Despite their names, startSEC and endSEC are not Unix seconds."

// systemJobDirectory points callers from what they want to do to the dedicated command that does it.
const systemJobDirectory = `Dedicated commands (prefer these; they fix the job and action for you):
  mark transactions synced without syncing to the ERP:  org admin system-jobs mark-reconciled
  undo mark-synced:                                      org admin system-jobs mark-unreconciled
  sync (send) categorized transactions to the ERP:       org admin system-jobs reconcile
  ignore / unignore transactions:                        org admin system-jobs ignore | unignore
  uncategorize transactions:                             org admin system-jobs uncategorize
  delete transactions:                                   org admin system-jobs delete-transactions
  reprice failed / priced and ready transactions:        org admin system-jobs reprice-failed | reprice-ready
  accept or ignore Needs Review transactions:            org admin system-jobs process-needs-review
  reprocess rollup parents for one wallet:               org admin system-jobs reprocess-rollups`

// unknownSystemJobMessage is static: it never repeats the submitted id, so it is safe to display.
var unknownSystemJobMessage = "systemJobId must be one of: " + strings.Join(knownSystemJobIDs, ", ") +
	". For bulk-transaction, action must be one of: " + strings.Join(bulkTransactionActions, ", ") +
	". To mark transactions synced without syncing, use `org admin system-jobs mark-reconciled`; to undo it, `org admin system-jobs mark-unreconciled`; to really sync to the ERP, `org admin system-jobs reconcile`. Run `org admin system-jobs run --help` for every job. Nothing was submitted."

// requireKnownSystemJob refuses a generic `run` whose systemJobId is missing or not a job the platform accepts,
// so an invented job or action fails locally, in dry-run too, instead of at the server or later in the job.
func requireKnownSystemJob(operation adminOperation, input map[string]any) error {
	if operation.Document != gqlSystemJobRun || operation.Defaults["systemJobId"] != nil {
		return nil
	}
	jobID, _ := input["systemJobId"].(string)
	if systemJobContract(jobID) == nil {
		if _, present := input["systemJobId"]; !present {
			return op.NewValidationError("systemJobId is required. " + unknownSystemJobMessage)
		}
		return op.NewValidationError("systemJobId is not a known system job. " + unknownSystemJobMessage)
	}
	return nil
}

func walletScopeField(description string) op.PayloadField {
	return op.PayloadField{Name: "walletId", Type: op.PayloadString, Description: description}
}

func dateFields(subject string) []op.PayloadField {
	return []op.PayloadField{
		{Name: "startSEC", Type: op.PayloadDate, Description: "First day to " + subject + ", YYYY-MM-DD in the organization's timezone. Omit for no lower bound.", Hint: "for example 2026-04-01"},
		{Name: "endSEC", Type: op.PayloadDate, Description: "Last day to " + subject + ", YYYY-MM-DD in the organization's timezone, inclusive. Omit for no upper bound.", Hint: "for example 2026-10-15"},
	}
}

// bulkTransactionPayload is the contract for the bulk-transaction job behind delete-transactions, ignore,
// unignore, reconcile, mark-reconciled, mark-unreconciled, uncategorize, and generic run.
func bulkTransactionPayload() *op.Payload {
	fields := []op.PayloadField{
		{Name: "action", Type: op.PayloadString, Required: true, Enum: bulkTransactionActions, Description: "Bulk action to apply. markReconciled marks transactions synced without sending them to the ERP; reconcile really syncs them."},
		walletScopeField("ID of one wallet to limit the job to. Omit for all wallets."),
	}
	fields = append(fields, dateFields("include")...)
	fields = append(fields,
		op.PayloadField{Name: "transactionType", Type: op.PayloadString, Description: "Read only by the ignore action: trade or categorizedInternalTransfer limits it to those transactions. Defaults to all."},
		op.PayloadField{Name: "allowReconciledModification", Type: op.PayloadBoolean, Description: "Read only by the ignore action, where true removes the reconciliation-status filter. Other actions ignore it. Defaults to false."},
		op.PayloadField{Name: "allowCategorizedModification", Type: op.PayloadBoolean, Description: "Read only by the ignore action, where true removes the categorization-status filter. Other actions ignore it. Defaults to false."},
		op.PayloadField{Name: "dataSourceId", Type: op.PayloadString, Description: "Uploaded CSV data source. Required for csvBulkAction."},
		op.PayloadField{Name: "matchCriteria", Type: op.PayloadObject, Description: "Transaction matching filters (fromAddress, toAddress, direction, tokens, methodId, metadataRule). Needs the system-job-transaction-matching-filters feature."},
	)
	return &op.Payload{
		Description: "Apply one bulk action to the matching transactions. " + systemJobDateRule + " Omit walletId for all wallets.",
		Fields:      fields,
		Check: func(values map[string]any) string {
			if problem := startNotAfterEnd(values); problem != "" {
				return problem
			}
			if values["action"] == "csvBulkAction" {
				if source, _ := values["dataSourceId"].(string); strings.TrimSpace(source) == "" {
					return "dataSourceId is required when action is csvBulkAction"
				}
			}
			return ""
		},
	}
}

func processNeedsReviewPayload() *op.Payload {
	fields := []op.PayloadField{
		{Name: "resolution", Type: op.PayloadString, Required: true, Enum: []string{"accept", "ignore"}, Description: "accept applies the pending Needs Review changes; ignore discards them."},
		{Name: "transactionState", Type: op.PayloadString, Required: true, Enum: []string{"open-needs-review", "new-needs-review", "closed-needs-review"}, Description: "Which Needs Review state to process."},
		walletScopeField("ID of one wallet to limit the job to. Omit for all wallets."),
	}
	fields = append(fields, dateFields("process")...)
	return &op.Payload{
		Description: "Accept or ignore pending Needs Review changes. " + systemJobDateRule + " Omit walletId for all wallets.",
		Fields:      fields,
		Check:       startNotAfterEnd,
	}
}

func deleteWalletPayload() *op.Payload {
	return &op.Payload{
		Description: "Delete one wallet and its transactions.",
		Fields: []op.PayloadField{
			{Name: "walletId", Type: op.PayloadString, Required: true, Description: "ID of the wallet to delete.", Hint: "one wallet ID string"},
		},
	}
}

func payloadHasField(payload *op.Payload, name string) bool {
	for _, field := range payload.Fields {
		if field.Name == name {
			return true
		}
	}
	return false
}

// fixedJobPayload is the contract a fixed-job command publishes: its job's contract without the action (which
// the command fixes), and with fields the command defaults no longer required of the caller.
func fixedJobPayload(payload *op.Payload, defaults map[string]any) *op.Payload {
	published := *payload
	published.Fields = make([]op.PayloadField, 0, len(payload.Fields))
	for _, field := range payload.Fields {
		if field.Name == "action" {
			continue
		}
		if _, defaulted := defaults[field.Name]; defaulted {
			field.Required = false
		}
		published.Fields = append(published.Fields, field)
	}
	return &published
}

// withJobContract attaches the contract of the operation's fixed job, so hosted adapters see the same rules the
// CLI enforces through adminPayloadFor.
func withJobContract(operation adminOperation) adminOperation {
	jobID, _ := operation.Defaults["systemJobId"].(string)
	if payload := systemJobContract(jobID); payload != nil {
		operation.Payload = fixedJobPayload(payload, operation.Defaults)
	}
	return operation
}

const systemJobDryRunNote = "Use --dry-run to check a payload: an invalid payload fails in dry-run with the\nsame message as the real call, and nothing is sent."

// bulkTransactionLong is the help for a dedicated bulk-transaction command.
func bulkTransactionLong(summary, action, use string) string {
	return summary + `

Runs the bulk-transaction job with action ` + action + `. The job and action are fixed
by this command and cannot be changed in the payload.

Payload (--data or --input; every field is optional):
  walletId                      one wallet ID; omit for all wallets
  startSEC, endSEC              YYYY-MM-DD in the organization's timezone, inclusive
                                whole days; omit either for no bound on that side.
                                Despite the names these are not Unix seconds.
  transactionType               ignore only: trade or categorizedInternalTransfer limits the
                                job to those; other actions ignore it; defaults to all
  allowReconciledModification   ignore only: true removes the reconciliation-status filter;
                                other actions ignore it; default false
  allowCategorizedModification  ignore only: true removes the categorization-status filter;
                                other actions ignore it; default false
  matchCriteria                 matching filters object (feature-gated)

` + systemJobDryRunNote + `

Example:
  bitwave org admin system-jobs ` + use + ` \
    --data '{"walletId":"WALLET_ID","startSEC":"2026-01-01","endSEC":"2026-03-31"}' --dry-run`
}

func systemJobRunLong() string {
	return `Run a system job chosen by systemJobId. Prefer the dedicated commands below: they
fix the job and action, so a typo cannot start the wrong job.

systemJobId is required and must be one of:
  bulk-transaction                                requires action (below); optional walletId, startSEC,
                                                  endSEC, transactionType, allowReconciledModification,
                                                  allowCategorizedModification, matchCriteria
  delete-wallet                                   requires walletId
  reprice-failed-to-price-transactions            optional walletId, startSEC, endSEC
  reprice-priced-and-ready-to-price-transactions  optional walletId, startSEC, endSEC
  process-needs-review-transactions               requires resolution (accept | ignore) and
                                                  transactionState (open-needs-review |
                                                  new-needs-review | closed-needs-review);
                                                  optional walletId, startSEC, endSEC
  reprocess-rollup-parent-txns                    requires walletId, startSEC, endSEC

bulk-transaction actions:
  delete            delete matching transactions
  markReconciled    mark transactions synced (reconciled) without sending them to the ERP
  markUnreconciled  undo mark-synced
  reconcile         sync (send) categorized transactions to the connected ERP; a real sync
  uncategorize      remove categorization
  ignore, unignore  ignore or unignore transactions
  csvBulkAction     apply an uploaded CSV; requires dataSourceId

Dates (startSEC, endSEC) are calendar days written YYYY-MM-DD in the organization's
timezone, inclusive of both whole days. Despite their names they are not Unix
seconds. Omit walletId for all wallets (except reprocess-rollup-parent-txns,
which needs one wallet).

` + systemJobDirectory + `

` + systemJobDryRunNote + `

Example:
  bitwave org admin system-jobs run \
    --data '{"systemJobId":"bulk-transaction","action":"markReconciled","startSEC":"2026-01-01","endSEC":"2026-03-31"}' --dry-run`
}

func repriceLong(summary, use string) string {
	return summary + `

Payload (--data or --input; every field is optional):
  walletId          one wallet ID; omit for all wallets
  startSEC, endSEC  YYYY-MM-DD in the organization's timezone, inclusive whole days;
                    omit either for no bound. Not Unix seconds.

` + systemJobDryRunNote + `

Example:
  bitwave org admin system-jobs ` + use + ` --data '{"startSEC":"2026-01-01","endSEC":"2026-03-31"}' --dry-run`
}

func processNeedsReviewLong() string {
	return `Accept or ignore the pending changes on Needs Review transactions.

Payload (--data or --input):
  resolution        accept (apply the pending changes) or ignore (discard them); default accept
  transactionState  open-needs-review, new-needs-review, or closed-needs-review;
                    default open-needs-review
  walletId          one wallet ID; omit for all wallets
  startSEC, endSEC  YYYY-MM-DD in the organization's timezone, inclusive whole days;
                    omit either for no bound. Not Unix seconds.

` + systemJobDryRunNote + `

Example:
  bitwave org admin system-jobs process-needs-review \
    --data '{"resolution":"accept","transactionState":"open-needs-review","walletId":"WALLET_ID"}' --dry-run`
}

func reprocessRollupsLong() string {
	return `Reprocess rollup-parent transactions for exactly one wallet and date range, using
the organization's existing rollup rules.

Payload (--data or --input):
  walletId                         required; one wallet ID (start one job per wallet)
  startSEC, endSEC                 required; YYYY-MM-DD in the organization's timezone,
                                   inclusive whole days. Not Unix seconds.
  processNewTxns                   default true
  updateChildrenForExistingParent  default false

` + systemJobDryRunNote + `

Example:
  bitwave org admin system-jobs reprocess-rollups \
    --data '{"walletId":"WALLET_ID","startSEC":"2026-04-01","endSEC":"2026-10-15"}' --dry-run`
}

func withLong(operation adminOperation, long string) adminOperation {
	operation.Long = long
	return operation
}

// bulkTransactionCommand builds a dedicated bulk-transaction command with its contract and help.
func bulkTransactionCommand(name, use, action, short string) adminOperation {
	operation := withDefaults(adminGraphQLOperation("system-jobs", name, use, short, gqlSystemJobRun), bulkTransactionDefaults(action))
	return withLong(withJobContract(operation), bulkTransactionLong(short+".", action, use))
}
