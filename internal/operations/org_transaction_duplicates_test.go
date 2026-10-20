package operations

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bitwave-io/bitwave-cli/internal/orgreports"
)

func TestDuplicateKeyReducesAnIdToItsOnChainHash(t *testing.T) {
	hash := "0x" + strings.Repeat("ab", 32)
	bare := strings.Repeat("ab", 32)
	for id, want := range map[string]string{
		hash:                                  bare,
		"BASE." + hash:                        bare,
		"BERACHAIN." + hash + "?id=5d14-45b7": bare,
		hash + "_import3":                     bare,
		strings.ToUpper(hash):                 bare, // hex is case-insensitive
		bare:                                  bare, // a missing 0x is the same hash
		"4MkxKMkXFPxHRzvQtBsdn3Dn37UaVQzpcFg5SyD1wSKBNTXeAEFCjqpTBPqept9KFx9Tycto9wspspfY9MxMnBm5": "4MkxKMkXFPxHRzvQtBsdn3Dn37UaVQzpcFg5SyD1wSKBNTXeAEFCjqpTBPqept9KFx9Tycto9wspspfY9MxMnBm5",
		"3M8M0GzpRpu0z7MlMmqQ_2026_10_15_23_ROLLUPS_BASED_ON_FROM_ADDRESS":                         "",
		"manual-1ec93970-54ee-4e32-a38d-ec2a042fe135":                                              "",
		"import:csv:row-42": "",
		"":                  "",
	} {
		if got := duplicateKey(id); got != want {
			t.Errorf("duplicateKey(%q) = %q, want %q", id, got, want)
		}
	}
	// Base58 keeps its case: these are two different Solana signatures.
	a := "4MkxKMkXFPxHRzvQtBsdn3Dn37UaVQzpcFg5SyD1wSKBNTXeAEFCjqpTBPqept9KFx9Tycto9wspspfY9MxMnBm5"
	if duplicateKey(a) == duplicateKey(strings.ToLower(a)) {
		t.Error("base58 ids must not be case-folded")
	}
}

type testLine struct {
	op, amount, currency, wallet, from, to string
}

func dupRow(id, timestamp string, lines ...testLine) duplicateRow {
	row := duplicateRow{compactTransaction: compactTransaction{ID: id, Timestamp: timestamp, CategorizationStatus: "Uncategorized", ReconciliationStatus: "Unreconciled"}}
	for i, l := range lines {
		row.Lines = append(row.Lines, compactTransactionLine{
			Line: i, Operation: l.op, Amount: json.RawMessage(l.amount), AmountCurrencyID: l.currency,
			WalletID: l.wallet, From: l.from, To: l.to,
		})
	}
	row.LineCount = len(row.Lines)
	return row
}

func TestClassifyPairRecognisesEveryKindSeenInRealData(t *testing.T) {
	send := testLine{"WITHDRAW", "-5", "COIN.1", "w-a", "0xAAA", "0xBBB"}
	fee := testLine{"FEE", "-0.0001", "COIN.1", "w-a", "0xAAA", "SYSTEM"}
	receive := testLine{"DEPOSIT", "5", "COIN.1", "w-b", "0xaaa", "0xbbb"}
	for _, test := range []struct {
		name           string
		a, b           duplicateRow
		kind           string
		recommendation string
	}{
		{"transfer legs in two wallets", dupRow("a", "1", send, fee), dupRow("b", "2", receive), "transfer-legs", recommendCombine},
		{"amount as a quoted string still matches", dupRow("a", "1", send), dupRow("b", "2", testLine{"DEPOSIT", `"5.0"`, "COIN.1", "w-b", "0xaaa", "0xbbb"}), "transfer-legs", recommendCombine},
		{"fee recorded in another wallet", dupRow("a", "1", send), dupRow("b", "2", testLine{"FEE", "-0.0001", "COIN.1", "w-fee", "0xAAA", "SYSTEM"}), "fee-leg", recommendCombineAfterReview},
		{"same send in two wallets sharing an address", dupRow("a", "1", send, fee), dupRow("b", "2", testLine{"WITHDRAW", "-5", "COIN.1", "w-b", "0xaaa", "0xbbb"}), "wallet-overlap", recommendDoNotCombine},
		{"same receive in two wallets sharing an address", dupRow("a", "1", receive), dupRow("b", "2", testLine{"DEPOSIT", "5", "COIN.1", "w-c", "0xAAA", "0xBBB"}), "wallet-overlap", recommendDoNotCombine},
		{"two deposits to different recipients", dupRow("a", "1", receive), dupRow("b", "2", testLine{"DEPOSIT", "5", "COIN.1", "w-c", "0xaaa", "0xccc"}), "distinct-recipients", recommendNotDuplicates},
		{"two withdrawals from different senders", dupRow("a", "1", send), dupRow("b", "2", testLine{"WITHDRAW", "-5", "COIN.1", "w-b", "0xddd", "0xbbb"}), "distinct-senders", recommendNotDuplicates},
		{"send and receive of different assets", dupRow("a", "1", send), dupRow("b", "2", testLine{"DEPOSIT", "4.9", "COIN.2", "w-b", "", ""}), "mixed-assets", recommendReview},
		{"send and receive with different amounts", dupRow("a", "1", send), dupRow("b", "2", testLine{"DEPOSIT", "4.5", "COIN.1", "w-b", "0xaaa", "0xbbb"}), "amount-mismatch", recommendReview},
		{"send and receive in the same wallet", dupRow("a", "1", send), dupRow("b", "2", testLine{"DEPOSIT", "5", "COIN.1", "w-a", "0xaaa", "0xbbb"}), "same-wallet-legs", recommendReview},
		{"several legs on one side", dupRow("a", "1", send, send), dupRow("b", "2", receive), "unclassified", recommendReview},
		{"nothing but fees on both sides", dupRow("a", "1", fee), dupRow("b", "2", fee), "unclassified", recommendReview},
	} {
		t.Run(test.name, func(t *testing.T) {
			kind, recommendation, reason := classifyPair(test.a, test.b)
			if kind != test.kind || recommendation != test.recommendation {
				t.Fatalf("classifyPair = %q/%q (%s), want %q/%q", kind, recommendation, reason, test.kind, test.recommendation)
			}
			if reason == "" {
				t.Fatal("every classification carries a reason")
			}
		})
	}
}

func TestClassifyDuplicateGroupDowngradesBlockedCombines(t *testing.T) {
	send := testLine{"WITHDRAW", "-5", "COIN.1", "w-a", "0xAAA", "0xBBB"}
	receive := testLine{"DEPOSIT", "5", "COIN.1", "w-b", "0xaaa", "0xbbb"}
	a := dupRow("a", "1", send)
	b := dupRow("b", "2", receive)
	if g := classifyDuplicateGroup("h", []duplicateRow{a, b}); g.Recommendation != recommendCombine || g.NextCommand == "" || len(g.Blockers) != 0 {
		t.Fatalf("clean pair = %#v", g)
	}
	b.CategorizationStatus = "Categorized"
	g := classifyDuplicateGroup("h", []duplicateRow{a, b})
	if g.Recommendation != recommendReview || g.Kind != "transfer-legs" {
		t.Fatalf("a categorized leg must downgrade the combine to review: %#v", g)
	}
	if len(g.Blockers) != 1 || g.Blockers[0] != "categorized" || g.NextCommand != "" {
		t.Fatalf("blockers = %v next = %q", g.Blockers, g.NextCommand)
	}
	a.ReconciliationStatus = "Reconciled"
	a.Ignored = true
	g = classifyDuplicateGroup("h", []duplicateRow{a, b})
	if strings.Join(g.Blockers, ",") != "categorized,ignored,reconciled" {
		t.Fatalf("blockers = %v", g.Blockers)
	}
	three := classifyDuplicateGroup("h", []duplicateRow{a, b, dupRow("c", "3", receive)})
	if three.Kind != "multiple-recordings" || three.Recommendation != recommendReview {
		t.Fatalf("three rows = %#v", three)
	}
}

func TestGroupDuplicatesSkipsCombinedRowsAndIdsWithoutAHash(t *testing.T) {
	hash := "0x" + strings.Repeat("cd", 32)
	parent := dupRow("parent", "0", testLine{"WITHDRAW", "-1", "COIN.1", "w-a", "", ""})
	parent.isCombinedParent = true
	child := dupRow("BASE."+hash, "1", testLine{"WITHDRAW", "-1", "COIN.1", "w-a", "", ""})
	child.combinedIntoTxnID = "parent"
	rows := []duplicateRow{
		parent, child,
		dupRow("manual-1", "2"),
		dupRow(hash+"?id=x", "3", testLine{"WITHDRAW", "-5", "COIN.1", "w-a", "0xaaa", "0xbbb"}),
		dupRow("BASE."+hash, "4", testLine{"DEPOSIT", "5", "COIN.1", "w-b", "0xaaa", "0xbbb"}),
		dupRow("0x"+strings.Repeat("ef", 32), "5", testLine{"DEPOSIT", "1", "COIN.1", "w-b", "", ""}), // alone: not a group
	}
	groups, skipped := groupDuplicates(rows)
	if skipped["alreadyCombined"] != 2 || skipped["noHash"] != 1 {
		t.Fatalf("skipped = %v", skipped)
	}
	if len(groups) != 1 || len(groups[0].Transactions) != 2 || groups[0].Hash != strings.Repeat("cd", 32) {
		t.Fatalf("groups = %#v", groups)
	}
	if groups[0].Recommendation != recommendCombine {
		t.Fatalf("group = %#v", groups[0])
	}
}

type duplicatesTestPage struct {
	rows      []map[string]any
	nextToken string
}

// duplicatesTestServer serves the org settings and a sequence of search
// pages keyed by the next-page token the client sends.
func duplicatesTestServer(t *testing.T, pages map[string]duplicatesTestPage) *[]orgreports.TransactionSearchRequest {
	t.Helper()
	var requests []orgreports.TransactionSearchRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v3/orgs/org-1":
			_, _ = w.Write([]byte(`{"id":"org-1","timezone":"Europe/Nicosia"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v3/orgs/org-1/transactions/search":
			var request orgreports.TransactionSearchRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode search: %v", err)
			}
			requests = append(requests, request)
			page, ok := pages[request.NextToken]
			if !ok {
				t.Errorf("unexpected page token %q", request.NextToken)
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"transactions": page.rows, "nextToken": page.nextToken})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("BITWAVE_BASE_URL_CORE", server.URL)
	t.Setenv("BITWAVE_TOKEN", "token")
	return &requests
}

func searchRow(id, timestamp string, isCombinedParent bool, lines ...testLine) map[string]any {
	var out []map[string]any
	for i, l := range lines {
		out = append(out, map[string]any{"line": i, "operation": l.op, "amount": json.Number(l.amount), "amountCurrencyId": l.currency, "walletId": l.wallet, "from": l.from, "to": l.to})
	}
	return map[string]any{
		"id": id, "timestamp": timestamp, "transactionType": "Send", "state": "priced",
		"categorizationStatus": "Uncategorized", "reconciliationStatus": "Unreconciled", "ignored": false,
		"isCombinedParent": isCombinedParent, "lines": out,
	}
}

func runDuplicates(t *testing.T, args ...string) (map[string]any, error) {
	t.Helper()
	cmd := testDefinition(newDuplicateTransactionsCmd())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	var result map[string]any
	if out.Len() > 0 {
		if jsonErr := json.Unmarshal(out.Bytes(), &result); jsonErr != nil {
			t.Fatalf("output is not JSON: %v\n%s", jsonErr, out.String())
		}
	}
	return result, err
}

func TestDuplicatesPagesTheSearchAndReturnsOnlyTheGroups(t *testing.T) {
	hash := "0x" + strings.Repeat("ab", 32)
	send := testLine{"WITHDRAW", "-5", "COIN.1", "w-a", "0xaaa", "0xbbb"}
	receive := testLine{"DEPOSIT", "5", "COIN.1", "w-b", "0xaaa", "0xbbb"}
	requests := duplicatesTestServer(t, map[string]duplicatesTestPage{
		"": {rows: []map[string]any{
			searchRow("BERACHAIN."+hash+"?id=1", "2026-01-01T00:00:00Z", false, send),
			searchRow("ROLLUP_2026_10_15_23_ROLLUPS_BASED_ON_FROM_ADDRESS", "2026-01-01T00:00:01Z", true, send),
		}, nextToken: "page-2"},
		"page-2": {rows: []map[string]any{
			searchRow("BERACHAIN."+hash, "2026-01-02T00:00:00Z", false, receive),
			searchRow("0x"+strings.Repeat("ef", 32), "2026-01-03T00:00:00Z", false, receive),
		}},
	})
	result, err := runDuplicates(t, "--org", "org-1", "--stage", "needs-categorization", "--page-size", "2", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 2 {
		t.Fatalf("expected two search pages, got %d", len(*requests))
	}
	first := (*requests)[0]
	if first.Filters.Stage != "needs-categorization" || first.Limit != 2 || first.SortDirection != "asc" || first.Timezone != "Europe/Nicosia" {
		t.Fatalf("first request = %#v", first)
	}
	if (*requests)[1].NextToken != "page-2" {
		t.Fatalf("second request did not carry the page token: %#v", (*requests)[1])
	}
	if result["scanned"].(float64) != 4 || result["truncated"] != false {
		t.Fatalf("scanned/truncated = %v/%v", result["scanned"], result["truncated"])
	}
	if _, present := result["transactions"]; present {
		t.Fatal("the scanned rows must not be returned")
	}
	groups := result["groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("groups = %#v", groups)
	}
	group := groups[0].(map[string]any)
	if group["recommendation"] != recommendCombine || group["kind"] != "transfer-legs" {
		t.Fatalf("group = %#v", group)
	}
	if !strings.HasPrefix(group["nextCommand"].(string), "bitwave transaction combine ") || !strings.Contains(group["nextCommand"].(string), "--dry-run") {
		t.Fatalf("nextCommand = %q", group["nextCommand"])
	}
	if result["skipped"].(map[string]any)["alreadyCombined"].(float64) != 1 {
		t.Fatalf("the rollup parent must be counted as skipped: %#v", result["skipped"])
	}
	summary := result["summary"].(map[string]any)
	if summary["groups"].(float64) != 1 || summary["rowsInGroups"].(float64) != 2 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestDuplicatesStopsAtMaxRowsAndSaysSo(t *testing.T) {
	requests := duplicatesTestServer(t, map[string]duplicatesTestPage{
		"":       {rows: []map[string]any{searchRow("0x"+strings.Repeat("ab", 32), "2026-01-01T00:00:00Z", false)}, nextToken: "page-2"},
		"page-2": {rows: []map[string]any{searchRow("0x"+strings.Repeat("cd", 32), "2026-01-02T00:00:00Z", false)}, nextToken: "page-3"},
	})
	result, err := runDuplicates(t, "--org", "org-1", "--max-rows", "2", "--page-size", "1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 2 {
		t.Fatalf("the scan must stop once max-rows is reached, got %d requests", len(*requests))
	}
	if result["truncated"] != true || result["nextToken"] != "page-3" {
		t.Fatalf("truncated/nextToken = %v/%v", result["truncated"], result["nextToken"])
	}
	if !strings.Contains(result["warning"].(string), "Only the first 2 rows") {
		t.Fatalf("warning = %q", result["warning"])
	}
}

func TestDuplicatesValidatesItsFlagsBeforeAnyRequest(t *testing.T) {
	requests := duplicatesTestServer(t, nil)
	for _, test := range []struct{ args, message string }{
		{"--max-rows 0", "--max-rows must be between 1 and 10000"},
		{"--max-rows 10001", "--max-rows must be between 1 and 10000"},
		{"--page-size 101", "--page-size must be between 1 and 100"},
		{"--from 2026-01-01", "--from and --to must be supplied together"},
	} {
		_, err := runDuplicates(t, append([]string{"--org", "org-1", "--json"}, strings.Fields(test.args)...)...)
		if err == nil || !strings.Contains(err.Error(), test.message) {
			t.Errorf("%s: err = %v, want %q", test.args, err, test.message)
		}
	}
	if len(*requests) != 0 {
		t.Fatal("invalid flags must not reach the server")
	}
}
