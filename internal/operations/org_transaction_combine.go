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
	var groups []string
	cmd := &op.Definition{
		Use:   "combine [TRANSACTION_ID TRANSACTION_ID...]",
		Short: "Combine several recordings of one on-chain transaction into one parent",
		Long: `Combine two or more transactions into one parent (PUT
/v3/orgs/{orgId}/transactions/combine). The parent starts uncategorized and
unreconciled. The server refuses reconciled, already-combined and closed-period
transactions. Requires --yes; --dry-run prints the request.

Positional ids form one parent. Repeat --group ID,ID[,ID] to combine several
groups in one confirmed call: one request and one result per group.`,
		Example: `  bitwave transaction combine TXN_ID_1 TXN_ID_2 --dry-run --json
  bitwave transaction combine --group A1,A2 --group B1,B2 --yes --json`,
		Args: op.ArbitraryArgs,
		RunE: func(cmd *op.Call, args []string) error {
			if len(groups) > 0 {
				if len(args) > 0 {
					return mutationError(cmd, combineOperation, f.jsonOutput, errors.New("pass transaction IDs either as arguments or with --group, not both"))
				}
				return runCombineGroups(cmd, groups, f)
			}
			return runCombineTransactions(cmd, uniqueNonEmpty(args), f)
		},
	}
	addMutationFlags(cmd, &f)
	cmd.Flags().StringArrayVar(&groups, "group", nil, "Comma-separated transaction IDs forming one parent (repeatable)")
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

// maxCombineGroups bounds one batch call; larger batches are split by the caller.
const maxCombineGroups = 100

type combineGroupResult struct {
	SourceTransactionIDs  []string `json:"sourceTransactionIds"`
	Status                string   `json:"status"`
	CombinedTransactionID string   `json:"combinedTransactionId,omitempty"`
	Error                 string   `json:"error,omitempty"`
}

type combineBatchResult struct {
	Groups    int                  `json:"groups"`
	Succeeded int                  `json:"succeeded"`
	Failed    int                  `json:"failed"`
	Results   []combineGroupResult `json:"results"`
	Note      string               `json:"note"`
}

// parseCombineGroups turns repeated --group values into validated id lists.
// Each group needs two distinct ids, and no id may appear in two groups.
func parseCombineGroups(raw []string) ([][]string, error) {
	if len(raw) > maxCombineGroups {
		return nil, fmt.Errorf("at most %d groups per call", maxCombineGroups)
	}
	seen := map[string]int{}
	var groups [][]string
	for i, value := range raw {
		ids := uniqueNonEmpty(strings.Split(value, ","))
		for j := range ids {
			ids[j] = strings.TrimSpace(ids[j])
		}
		ids = uniqueNonEmpty(ids)
		if len(ids) < 2 {
			return nil, fmt.Errorf("--group %d needs at least two distinct transaction IDs", i+1)
		}
		for _, id := range ids {
			if other, dup := seen[id]; dup {
				return nil, fmt.Errorf("transaction %s is in --group %d and --group %d", id, other, i+1)
			}
			seen[id] = i + 1
		}
		groups = append(groups, ids)
	}
	return groups, nil
}

func runCombineGroups(cmd *op.Call, raw []string, f transactionMutationFlags) error {
	groups, err := parseCombineGroups(raw)
	if err != nil {
		return mutationError(cmd, combineOperation, f.jsonOutput, err)
	}
	orgID, err := resolveReportOrg(cmd.Context(), f.orgID)
	if err != nil {
		return mutationError(cmd, combineOperation, f.jsonOutput, err)
	}
	path := fmt.Sprintf("/v3/orgs/%s/transactions/combine", orgID)
	if f.dryRun {
		requests := make([]map[string]any, 0, len(groups))
		for _, ids := range groups {
			requests = append(requests, map[string]any{"method": "PUT", "path": path, "body": orgreports.TransactionIDsBody{TxnIDs: ids}})
		}
		return writeJSON(cmd.OutOrStdout(), mutationEnvelope{SchemaVersion: "1", Status: "preview", Operation: combineOperation, Organization: orgID, DryRun: true, Request: requests})
	}
	if !f.yes {
		return mutationError(cmd, combineOperation, f.jsonOutput, errors.New("refusing to change the organization without --yes (use --dry-run to preview)"))
	}
	client := newReportsClient(cmd.Context(), orgID)
	batch := combineBatchResult{Groups: len(groups), Results: make([]combineGroupResult, 0, len(groups)),
		Note: "Combined parents are uncategorized and unreconciled. Reverse one with: bitwave transaction uncombine PARENT_ID --yes"}
	// One request per group, in order: a refused group is reported and the
	// rest still run, so the caller never has to guess which ones landed.
	for _, ids := range groups {
		result := combineGroupResult{SourceTransactionIDs: ids}
		parent, err := client.CombineTransactions(cmd.Context(), orgID, ids)
		switch {
		case err != nil:
			result.Status, result.Error = "failed", err.Error()
		case strings.TrimSpace(parent.ID) == "":
			result.Status, result.Error = "failed", "the server returned no combined transaction id; check these transactions before retrying"
		default:
			result.Status, result.CombinedTransactionID = "success", parent.ID
		}
		if result.Status == "success" {
			batch.Succeeded++
		} else {
			batch.Failed++
		}
		batch.Results = append(batch.Results, result)
	}
	envelope := mutationEnvelope{SchemaVersion: "1", Status: "success", Operation: combineOperation, Organization: orgID, Result: batch}
	if batch.Failed > 0 {
		envelope.Status = "partial_failure"
		_ = writeJSON(cmd.OutOrStdout(), envelope)
		return fmt.Errorf("combine processed %d group(s): %d succeeded, %d failed", batch.Groups, batch.Succeeded, batch.Failed)
	}
	return outputMutation(cmd, f.jsonOutput, envelope, fmt.Sprintf("combine: %d group(s) combined\n", batch.Succeeded))
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
