package operations

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"

	op "github.com/bitwave-io/bitwave-cli/internal/operation"

	"github.com/bitwave-io/bitwave-cli/internal/orgreports"
)

// `transaction duplicates` finds transactions that record the same on-chain
// transaction more than once under slightly different ids, and says what to
// do with each group. The ids differ by a network prefix (BASE.0x…), a custody
// suffix (0x…?id=<uuid>) or an import suffix (0x…_import1), so an exact id
// match never finds them.
//
// The command pages the search API itself and returns only the groups, never
// the rows it scanned, so a caller (an LLM in particular) pays for the
// duplicates and not for the population. Grouping by hash is the easy part;
// the classification is what matters, because most same-hash pairs in real
// organizations are not duplicates to combine:
//
//   - the send leg in one wallet and the receive leg in another are one
//     transfer: combine them;
//   - the same send recorded in two wallets that share an address is a
//     wallet-overlap problem: combining it would double the amount;
//   - two deposits with the same hash to different recipients are two
//     transfers inside one transaction: leave them alone.

const (
	duplicatesDefaultPageSize = 100
	duplicatesDefaultMaxRows  = 2000
	duplicatesMaxRows         = 10000

	recommendCombine            = "combine"
	recommendCombineAfterReview = "combine-after-review"
	recommendReview             = "review"
	recommendDoNotCombine       = "do-not-combine"
	recommendNotDuplicates      = "not-duplicates"
)

type transactionDuplicatesFlags struct {
	orgID, from, to, stage                  string
	wallets, assets, types, states          []string
	categorization, reconciliation, ignored []string
	includeCombined                         bool
	maxRows, pageSize                       int
}

func newDuplicateTransactionsCmd() *op.Definition {
	var f transactionDuplicatesFlags
	cmd := &op.Definition{
		Use:   "duplicates",
		Short: "Find transactions that record one on-chain transaction more than once",
		Long: `Group transactions that share an on-chain hash under different ids (network
prefix, "?id=" suffix, "_importN" suffix). Read-only: pages the search API and
returns the groups only, each with kind, recommendation (combine,
combine-after-review, review, do-not-combine, not-duplicates), reason, blockers
and nextCommand. Stops at --max-rows and reports truncated with nextToken.`,
		Example: `  bitwave transaction duplicates --stage needs-categorization --json
  bitwave transaction duplicates --from 2026-01-01 --to 2026-03-31 --wallet Treasury --json`,
		Args: op.NoArgs,
		RunE: func(cmd *op.Call, _ []string) error { return runDuplicateTransactions(cmd, f) },
	}
	cmd.Flags().StringVar(&f.orgID, "org", "", "Organization ID override")
	cmd.Flags().StringVar(&f.from, "from", "", "Inclusive start date (YYYY-MM-DD; requires --to)")
	cmd.Flags().StringVar(&f.to, "to", "", "Inclusive end date (YYYY-MM-DD; requires --from)")
	cmd.Flags().StringVar(&f.stage, "stage", "", "Workflow stage, as the transaction UI's tabs use it: needs-categorization, to-be-synced, complete, ignored, or deleted")
	cmd.Flags().StringSliceVar(&f.wallets, "wallet", nil, "Wallet ID or exact name (repeatable)")
	cmd.Flags().StringSliceVar(&f.assets, "asset", nil, "Asset ID (repeatable)")
	cmd.Flags().StringSliceVar(&f.types, "type", nil, "Transaction type, such as send, receive, trade, or transfer")
	cmd.Flags().StringSliceVar(&f.states, "state", nil, "Transaction workflow state")
	cmd.Flags().StringSliceVar(&f.categorization, "categorization", nil, "Categorization status")
	cmd.Flags().StringSliceVar(&f.reconciliation, "reconciliation", nil, "Reconciliation status")
	cmd.Flags().StringSliceVar(&f.ignored, "ignored", nil, "Ignored status")
	cmd.Flags().BoolVar(&f.includeCombined, "include-combined", false, "Include combined transaction children")
	cmd.Flags().IntVar(&f.maxRows, "max-rows", duplicatesDefaultMaxRows, fmt.Sprintf("Stop scanning after this many rows (1-%d)", duplicatesMaxRows))
	cmd.Flags().IntVar(&f.pageSize, "page-size", duplicatesDefaultPageSize, "Rows fetched per search page (1-100)")
	cmd.Flags().Bool("json", true, "Emit machine-readable JSON (the only supported format)")
	return cmd
}

// duplicateRow is a compact transaction plus the two fields that say it is
// already part of a combine, which the compact shape leaves out.
type duplicateRow struct {
	compactTransaction
	isCombinedParent  bool
	combinedIntoTxnID string
}

type duplicateGroup struct {
	Hash           string               `json:"hash"`
	Kind           string               `json:"kind"`
	Recommendation string               `json:"recommendation"`
	Reason         string               `json:"reason"`
	Blockers       []string             `json:"blockers,omitempty"`
	NextCommand    string               `json:"nextCommand,omitempty"`
	Transactions   []compactTransaction `json:"transactions"`
}

type duplicatesSummary struct {
	Groups           int            `json:"groups"`
	RowsInGroups     int            `json:"rowsInGroups"`
	ByRecommendation map[string]int `json:"byRecommendation"`
}

func runDuplicateTransactions(cmd *op.Call, f transactionDuplicatesFlags) error {
	if f.pageSize < 1 || f.pageSize > 100 {
		return op.NewValidationError("--page-size must be between 1 and 100")
	}
	if f.maxRows < 1 || f.maxRows > duplicatesMaxRows {
		return op.NewValidationError(fmt.Sprintf("--max-rows must be between 1 and %d", duplicatesMaxRows))
	}
	if (f.from == "") != (f.to == "") {
		return op.NewValidationError("--from and --to must be supplied together")
	}
	if f.from != "" {
		if err := validateExportDateRange(f.from, f.to, false); err != nil {
			return err
		}
	}
	orgID, err := resolveReportOrg(cmd.Context(), f.orgID)
	if err != nil {
		return err
	}
	client := newReportsClient(cmd.Context(), orgID)
	org, err := client.Org(cmd.Context(), orgID)
	if err != nil {
		return fmt.Errorf("load organization settings: %w", err)
	}
	var walletIDs []string
	if len(uniqueNonEmpty(f.wallets)) > 0 {
		wallets, err := client.Wallets(cmd.Context(), orgID)
		if err != nil {
			return fmt.Errorf("discover wallets: %w", err)
		}
		if walletIDs, err = resolveWalletRefs(f.wallets, wallets); err != nil {
			return err
		}
	}
	filters := orgreports.TransactionExportFilters{
		DateRange: optionalDateRange(f.from, f.to), WalletIDs: walletIDs, AssetIDs: uniqueNonEmpty(f.assets),
		TransactionTypes: uniqueNonEmpty(f.types), Stage: strings.TrimSpace(f.stage), States: uniqueNonEmpty(f.states),
		CategorizationStatuses: uniqueNonEmpty(f.categorization), ReconciliationStatuses: uniqueNonEmpty(f.reconciliation),
		IgnoredStatuses: uniqueNonEmpty(f.ignored), IncludeCombinedTransactions: f.includeCombined,
	}

	// Oldest first, so a truncated scan covers a contiguous, reportable range.
	request := orgreports.TransactionSearchRequest{Timezone: org.Timezone, Limit: f.pageSize, SortBy: "timestamp", SortDirection: "asc", Filters: filters}
	var rows []duplicateRow
	nextToken := ""
	for {
		request.NextToken = nextToken
		page, err := client.SearchTransactions(cmd.Context(), orgID, request)
		if err != nil {
			return fmt.Errorf("search transactions: %w", err)
		}
		rows = append(rows, duplicateRows(page.Transactions)...)
		nextToken = page.NextToken
		if nextToken == "" || len(page.Transactions) == 0 || len(rows) >= f.maxRows {
			break
		}
	}
	truncated := nextToken != ""

	groups, skipped := groupDuplicates(rows)
	summary := duplicatesSummary{Groups: len(groups), ByRecommendation: map[string]int{}}
	for _, g := range groups {
		summary.RowsInGroups += len(g.Transactions)
		summary.ByRecommendation[g.Recommendation]++
	}
	out := map[string]any{
		"schemaVersion": "1", "organization": orgID,
		"request":   map[string]any{"timezone": org.Timezone, "filters": filters, "pageSize": f.pageSize, "maxRows": f.maxRows},
		"scanned":   len(rows),
		"truncated": truncated,
		"skipped":   skipped,
		"summary":   summary,
		"groups":    groups,
		"warning": "Classification is a recommendation from the rows' legs, wallets and amounts. Confirm each group before combining; " +
			"combining resets categorization, and do-not-combine groups are a wallet setup problem, not a transaction problem.",
	}
	if truncated {
		out["nextToken"] = nextToken
		out["warning"] = fmt.Sprintf("Only the first %d rows were scanned; the filter matches more. Narrow the filter or raise --max-rows. ", len(rows)) + out["warning"].(string)
	}
	return writeJSON(cmd.OutOrStdout(), out)
}

func duplicateRows(items []json.RawMessage) []duplicateRow {
	compact := compactTransactions(items)
	rows := make([]duplicateRow, 0, len(compact))
	byID := map[string]duplicateRow{}
	for _, item := range items {
		var extra struct {
			ID                string `json:"id"`
			IsCombinedParent  bool   `json:"isCombinedParent"`
			CombinedIntoTxnID string `json:"combinedIntoTxnId"`
		}
		if json.Unmarshal(item, &extra) == nil {
			byID[extra.ID] = duplicateRow{isCombinedParent: extra.IsCombinedParent, combinedIntoTxnID: extra.CombinedIntoTxnID}
		}
	}
	for _, c := range compact {
		row := byID[c.ID]
		row.compactTransaction = c
		rows = append(rows, row)
	}
	return rows
}

// groupDuplicates buckets rows by normalized hash and classifies every bucket
// with more than one row. Rows that are already combined, or whose id carries
// no hash, are counted under skipped rather than silently dropped.
func groupDuplicates(rows []duplicateRow) ([]duplicateGroup, map[string]int) {
	skipped := map[string]int{"alreadyCombined": 0, "noHash": 0}
	buckets := map[string][]duplicateRow{}
	var order []string
	for _, row := range rows {
		if row.isCombinedParent || row.combinedIntoTxnID != "" {
			skipped["alreadyCombined"]++
			continue
		}
		key := duplicateKey(row.ID)
		if key == "" {
			skipped["noHash"]++
			continue
		}
		if _, seen := buckets[key]; !seen {
			order = append(order, key)
		}
		buckets[key] = append(buckets[key], row)
	}
	var groups []duplicateGroup
	for _, key := range order {
		if len(buckets[key]) < 2 {
			continue
		}
		groups = append(groups, classifyDuplicateGroup(key, buckets[key]))
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Transactions[0].Timestamp < groups[j].Transactions[0].Timestamp })
	return groups, skipped
}

var (
	// A network prefix is the uppercase token before the first dot: BASE.0x…
	networkPrefixRe = regexp.MustCompile(`^[A-Z0-9_-]+\.`)
	importSuffixRe  = regexp.MustCompile(`_import\d+$`)
	hexHashRe       = regexp.MustCompile(`^(0[xX])?[0-9a-fA-F]{32,}$`)
	base58HashRe    = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{32,}$`)
)

// duplicateKey reduces a transaction id to the on-chain hash it carries, or
// "" when it carries none. Hex hashes are case-insensitive and lose their 0x;
// base58 hashes (Solana and the like) keep their case, since there it is
// significant. Rollup parents and manual or import ids have no hash.
func duplicateKey(id string) string {
	if strings.Contains(id, "ROLLUPS_BASED_ON") {
		return ""
	}
	core := networkPrefixRe.ReplaceAllString(strings.TrimSpace(id), "")
	if i := strings.IndexByte(core, '?'); i >= 0 {
		core = core[:i]
	}
	core = importSuffixRe.ReplaceAllString(core, "")
	switch {
	case hexHashRe.MatchString(core):
		return strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(core, "0x"), "0X"))
	case base58HashRe.MatchString(core):
		return core
	}
	return ""
}

func classifyDuplicateGroup(key string, rows []duplicateRow) duplicateGroup {
	g := duplicateGroup{Hash: key, Transactions: make([]compactTransaction, 0, len(rows))}
	for _, row := range rows {
		g.Transactions = append(g.Transactions, row.compactTransaction)
	}
	sort.SliceStable(g.Transactions, func(i, j int) bool { return g.Transactions[i].Timestamp < g.Transactions[j].Timestamp })

	if len(rows) != 2 {
		g.Kind, g.Recommendation = "multiple-recordings", recommendReview
		g.Reason = fmt.Sprintf("%d transactions share this hash; inspect them by hand before combining any", len(rows))
	} else {
		g.Kind, g.Recommendation, g.Reason = classifyPair(rows[0], rows[1])
	}

	for _, row := range rows {
		g.Blockers = append(g.Blockers, duplicateBlockers(row)...)
	}
	sort.Strings(g.Blockers)
	if len(g.Blockers) > 0 && (g.Recommendation == recommendCombine || g.Recommendation == recommendCombineAfterReview) {
		g.Recommendation = recommendReview
		g.Reason += "; a transaction in the group is " + strings.Join(g.Blockers, ", ") + " and combining would reset or be refused"
	}
	if g.Recommendation == recommendCombine || g.Recommendation == recommendCombineAfterReview {
		ids := make([]string, 0, len(g.Transactions))
		for _, t := range g.Transactions {
			ids = append(ids, fmt.Sprintf("%q", t.ID))
		}
		g.NextCommand = "bitwave transaction combine " + strings.Join(ids, " ") + " --dry-run --json"
	}
	return g
}

type duplicateLeg struct {
	operation, currency, wallet, from, to string
	amount                                *big.Rat
}

// transferLegs returns a row's withdraw and deposit lines; fee lines are
// reported separately because a fee alone is not a transfer.
func transferLegs(row duplicateRow) (legs []duplicateLeg, fees int) {
	for _, line := range row.Lines {
		operation := strings.ToUpper(line.Operation)
		switch operation {
		case "WITHDRAW", "DEPOSIT":
			legs = append(legs, duplicateLeg{
				operation: operation, currency: line.AmountCurrencyID, wallet: line.WalletID,
				from: line.From, to: line.To, amount: parseLegAmount(line.Amount),
			})
		case "FEE":
			fees++
		}
	}
	return legs, fees
}

func parseLegAmount(raw json.RawMessage) *big.Rat {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if text == "" {
		return nil
	}
	value, ok := new(big.Rat).SetString(text)
	if !ok {
		return nil
	}
	return value
}

func sameAbsAmount(a, b *big.Rat) bool {
	if a == nil || b == nil {
		return false
	}
	return new(big.Rat).Abs(a).Cmp(new(big.Rat).Abs(b)) == 0
}

func classifyPair(a, b duplicateRow) (kind, recommendation, reason string) {
	legsA, feesA := transferLegs(a)
	legsB, feesB := transferLegs(b)
	switch {
	case len(legsA) == 0 && len(legsB) > 0 && feesA > 0, len(legsB) == 0 && len(legsA) > 0 && feesB > 0:
		return "fee-leg", recommendCombineAfterReview, "one transaction holds only the fee of the other's transfer, recorded in a different wallet; combine once the fee wallet is confirmed as the payer"
	case len(legsA) != 1 || len(legsB) != 1:
		return "unclassified", recommendReview, "the transactions do not pair up as one transfer leg each; inspect them by hand"
	}
	x, y := legsA[0], legsB[0]
	sameCurrency := x.currency == y.currency
	sameAmount := sameAbsAmount(x.amount, y.amount)
	sameWallet := x.wallet == y.wallet
	sameFrom := strings.EqualFold(x.from, y.from)
	sameTo := strings.EqualFold(x.to, y.to)

	if x.operation != y.operation {
		switch {
		case !sameCurrency:
			return "mixed-assets", recommendReview, "a send of one asset and a receive of another share the hash: a swap, bridge or import; decide whether it is one trade"
		case sameWallet:
			return "same-wallet-legs", recommendReview, "the send and receive legs sit in the same wallet; check whether this is a self-transfer or a mis-assigned leg"
		case !sameAmount:
			return "amount-mismatch", recommendReview, "the send and receive legs are the same asset but different amounts; check for a fee or a partial fill before combining"
		}
		return "transfer-legs", recommendCombine, "the send leg in one wallet and the receive leg in another are one transfer between two of your wallets"
	}
	switch {
	case sameCurrency && sameAmount && sameFrom && sameTo && !sameWallet:
		return "wallet-overlap", recommendDoNotCombine, "the same " + strings.ToLower(x.operation) + " is recorded in two wallets that share an address; combining would double the amount, so fix the wallet overlap or ignore one copy"
	case x.operation == "DEPOSIT" && !sameTo:
		return "distinct-recipients", recommendNotDuplicates, "two deposits with different recipients inside one transaction are separate transfers; leave them as they are"
	case x.operation == "WITHDRAW" && !sameFrom:
		return "distinct-senders", recommendNotDuplicates, "two withdrawals from different senders inside one transaction are separate transfers; leave them as they are"
	}
	return "unclassified", recommendReview, "two " + strings.ToLower(x.operation) + " legs that match on some fields but not all; inspect them by hand"
}

func duplicateBlockers(row duplicateRow) []string {
	var out []string
	if strings.EqualFold(row.ReconciliationStatus, "Reconciled") {
		out = append(out, "reconciled")
	}
	if row.Ignored {
		out = append(out, "ignored")
	}
	if row.CategorizationStatus != "" && !strings.EqualFold(row.CategorizationStatus, "Uncategorized") {
		out = append(out, "categorized")
	}
	return out
}
