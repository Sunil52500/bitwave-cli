package operations

import (
	"errors"
	"fmt"
	"strings"

	op "github.com/bitwave-io/bitwave-cli/internal/operation"

	"github.com/bitwave-io/bitwave-cli/internal/orgreports"
)

// Combine and uncombine use the same endpoints as the transaction UI's
// Combine and Un-combine actions. Combining merges several recordings of one
// on-chain transaction into a single parent; the parent starts uncategorized,
// so categorized children lose their categorization. Uncombine reverses it.
//
// Both follow the safety contract of every transaction write: --dry-run
// prints the exact request without contacting Bitwave, and --yes is required
// to send it. `bitwave transaction duplicates` is the read that finds and
// classifies candidate groups beforehand.

const (
	combineOperation   = "combine"
	uncombineOperation = "uncombine"
)

func newCombineTransactionsCmd() *op.Definition {
	var f transactionMutationFlags
	cmd := &op.Definition{
		Use:   "combine TRANSACTION_ID TRANSACTION_ID [TRANSACTION_ID...]",
		Short: "Combine several recordings of one on-chain transaction into one parent",
		Long: `Combine two or more transactions into one parent (PUT
/v3/orgs/{orgId}/transactions/combine). The parent starts uncategorized and
unreconciled. The server refuses reconciled, already-combined and closed-period
transactions. Requires --yes; --dry-run prints the request.`,
		Example: `  bitwave transaction duplicates --stage needs-categorization --json
  bitwave transaction combine TXN_ID_1 TXN_ID_2 --dry-run --json
  bitwave transaction combine TXN_ID_1 TXN_ID_2 --yes --json`,
		Args: op.MinimumNArgs(2),
		RunE: func(cmd *op.Call, args []string) error {
			return runCombineTransactions(cmd, uniqueNonEmpty(args), f)
		},
	}
	addMutationFlags(cmd, &f)
	return cmd
}

func newUncombineTransactionsCmd() *op.Definition {
	var f transactionMutationFlags
	cmd := &op.Definition{
		Use:   "uncombine TRANSACTION_ID [TRANSACTION_ID...]",
		Short: "Restore the children of combined parent transactions",
		Long: `Restore the children of each combined parent (PUT
/v3/orgs/{orgId}/transactions/uncombine). Requires --yes; --dry-run prints the request.`,
		Example: `  bitwave transaction uncombine PARENT_TXN_ID --dry-run --json
  bitwave transaction uncombine PARENT_TXN_ID --yes --json`,
		Args: op.MinimumNArgs(1),
		RunE: func(cmd *op.Call, args []string) error {
			return runUncombineTransactions(cmd, uniqueNonEmpty(args), f)
		},
	}
	addMutationFlags(cmd, &f)
	return cmd
}

type combineResult struct {
	CombinedTransactionID string   `json:"combinedTransactionId"`
	SourceTransactionIDs  []string `json:"sourceTransactionIds"`
	Note                  string   `json:"note"`
}

func runCombineTransactions(cmd *op.Call, ids []string, f transactionMutationFlags) error {
	// Deduplication can leave one id when the same id was given twice.
	if len(ids) < 2 {
		return mutationError(cmd, combineOperation, f.jsonOutput, errors.New("at least two distinct transaction IDs are required"))
	}
	orgID, err := resolveReportOrg(cmd.Context(), f.orgID)
	if err != nil {
		return mutationError(cmd, combineOperation, f.jsonOutput, err)
	}
	request := orgreports.TransactionIDsBody{TxnIDs: ids}
	preview := map[string]any{"method": "PUT", "path": fmt.Sprintf("/v3/orgs/%s/transactions/combine", orgID), "body": request}
	if f.dryRun {
		return writeJSON(cmd.OutOrStdout(), mutationEnvelope{SchemaVersion: "1", Status: "preview", Operation: combineOperation, Organization: orgID, DryRun: true, Request: preview})
	}
	if !f.yes {
		return mutationError(cmd, combineOperation, f.jsonOutput, errors.New("refusing to change the organization without --yes (use --dry-run to preview)"))
	}
	client := newReportsClient(cmd.Context(), orgID)
	result, err := client.CombineTransactions(cmd.Context(), orgID, ids)
	if err != nil {
		return mutationError(cmd, combineOperation, f.jsonOutput, fmt.Errorf("combine transactions: %w", err))
	}
	if strings.TrimSpace(result.ID) == "" {
		return mutationError(cmd, combineOperation, f.jsonOutput, errors.New("combine transactions: the server returned no combined transaction id; check the transactions before retrying"))
	}
	envelope := mutationEnvelope{SchemaVersion: "1", Status: "success", Operation: combineOperation, Organization: orgID, Result: combineResult{
		CombinedTransactionID: result.ID, SourceTransactionIDs: ids,
		Note: "The combined transaction is uncategorized and unreconciled. Reverse with: bitwave transaction uncombine " + result.ID + " --yes",
	}}
	return outputMutation(cmd, f.jsonOutput, envelope, fmt.Sprintf("combine: %d transactions combined into %s\n", len(ids), result.ID))
}

type uncombineResult struct {
	Processed int                                     `json:"processed"`
	Succeeded int                                     `json:"succeeded"`
	Failed    []orgreports.UncombineTransactionResult `json:"failed"`
	Results   []orgreports.UncombineTransactionResult `json:"results"`
}

func runUncombineTransactions(cmd *op.Call, ids []string, f transactionMutationFlags) error {
	if len(ids) == 0 {
		return mutationError(cmd, uncombineOperation, f.jsonOutput, errors.New("at least one combined transaction ID is required"))
	}
	orgID, err := resolveReportOrg(cmd.Context(), f.orgID)
	if err != nil {
		return mutationError(cmd, uncombineOperation, f.jsonOutput, err)
	}
	request := orgreports.TransactionIDsBody{TxnIDs: ids}
	preview := map[string]any{"method": "PUT", "path": fmt.Sprintf("/v3/orgs/%s/transactions/uncombine", orgID), "body": request}
	if f.dryRun {
		return writeJSON(cmd.OutOrStdout(), mutationEnvelope{SchemaVersion: "1", Status: "preview", Operation: uncombineOperation, Organization: orgID, DryRun: true, Request: preview})
	}
	if !f.yes {
		return mutationError(cmd, uncombineOperation, f.jsonOutput, errors.New("refusing to change the organization without --yes (use --dry-run to preview)"))
	}
	client := newReportsClient(cmd.Context(), orgID)
	results, err := client.UncombineTransactions(cmd.Context(), orgID, ids)
	if err != nil {
		return mutationError(cmd, uncombineOperation, f.jsonOutput, fmt.Errorf("uncombine transactions: %w", err))
	}
	summary := uncombineResult{Processed: len(results), Results: results, Failed: []orgreports.UncombineTransactionResult{}}
	for _, r := range results {
		if strings.EqualFold(r.Status, "fulfilled") {
			summary.Succeeded++
		} else {
			summary.Failed = append(summary.Failed, r)
		}
	}
	envelope := mutationEnvelope{SchemaVersion: "1", Status: "success", Operation: uncombineOperation, Organization: orgID, Result: summary}
	// The server answers per parent, so one bad id must not hide the others.
	if len(summary.Failed) > 0 || summary.Processed < len(ids) {
		envelope.Status = "partial_failure"
		_ = writeJSON(cmd.OutOrStdout(), envelope)
		return fmt.Errorf("uncombine processed %d of %d transaction(s): %d succeeded, %d failed", summary.Processed, len(ids), summary.Succeeded, len(summary.Failed))
	}
	return outputMutation(cmd, f.jsonOutput, envelope, fmt.Sprintf("uncombine: %d transaction(s) restored\n", summary.Succeeded))
}
