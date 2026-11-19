package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"

	op "github.com/bitwave-io/bitwave-cli/internal/operation"

	"github.com/bitwave-io/bitwave-cli/internal/orgreports"
)

func newOrgWalletsRollupCmd() *op.Definition {
	cmd := &op.Definition{Use: "rollup", Short: "Inspect or configure Babel wallet rollups"}
	cmd.AddCommand(newOrgWalletRollupGetCmd(), newOrgWalletRollupSetCmd())
	return cmd
}

func newOrgWalletRollupGetCmd() *op.Definition {
	var orgID string
	cmd := &op.Definition{
		Use: "get WALLET_ID_OR_NAME", Short: "Get a wallet's Babel rollup configuration", Args: op.ExactArgs(1),
		RunE: func(cmd *op.Call, args []string) error {
			orgID, err := resolveReportOrg(cmd.Context(), orgID)
			if err != nil {
				return err
			}
			client := newReportsClient(cmd.Context(), orgID)
			wallet, err := resolveOrganizationWallet(cmd, client, orgID, args[0])
			if err != nil {
				return err
			}
			rollup, err := client.WalletRollup(cmd.Context(), orgID, wallet.ID)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]any{"schemaVersion": "1", "organization": orgID, "wallet": wallet, "rollup": rollup})
		},
	}
	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID override")
	cmd.Flags().Bool("json", true, "Emit machine-readable JSON (the only supported format)")
	return cmd
}

func newOrgWalletRollupSetCmd() *op.Definition {
	var f transactionMutationFlags
	var input string
	cmd := &op.Definition{
		Use: "set WALLET_ID_OR_NAME", Short: "Create or replace a wallet's Babel rollup configuration", Args: op.ExactArgs(1),
		Long: walletRollupSetHelp,
		RunE: func(cmd *op.Call, args []string) error {
			operation := "set-wallet-rollup"
			request, err := readWalletRollupRequest(cmd.Context(), input, cmd.InOrStdin())
			if err != nil {
				return mutationError(cmd, operation, f.jsonOutput, err)
			}
			orgID, err := resolveReportOrg(cmd.Context(), f.orgID)
			if err != nil {
				return mutationError(cmd, operation, f.jsonOutput, err)
			}
			client := newReportsClient(cmd.Context(), orgID)
			wallet, err := resolveOrganizationWallet(cmd, client, orgID, args[0])
			if err != nil {
				return mutationError(cmd, operation, f.jsonOutput, err)
			}
			if err := bindWalletRollupAddress(&request, wallet); err != nil {
				return mutationError(cmd, operation, f.jsonOutput, err)
			}
			if f.dryRun {
				return writeJSON(cmd.OutOrStdout(), mutationEnvelope{SchemaVersion: "1", Status: "preview", Operation: operation, Organization: orgID, DryRun: true, Request: request})
			}
			if !f.yes {
				return mutationError(cmd, operation, f.jsonOutput, op.NewConfirmationRequired("refusing to change the wallet rollup without --yes"))
			}
			if err := client.UpsertWalletRollup(cmd.Context(), orgID, wallet.ID, request); err != nil {
				return mutationError(cmd, operation, f.jsonOutput, err)
			}
			return writeJSON(cmd.OutOrStdout(), mutationEnvelope{SchemaVersion: "1", Status: "success", Operation: operation, Organization: orgID, Result: map[string]any{"wallet": wallet, "ruleCount": len(request.Rules)}})
		},
	}
	addMutationFlags(cmd, &f)
	cmd.Flags().StringVarP(&input, "input", "i", "", "Complete wallet rollup JSON request, or - for stdin (required)")
	return cmd
}

func readWalletRollupRequest(ctx context.Context, path string, stdin io.Reader) (orgreports.WalletRollupRequest, error) {
	if strings.TrimSpace(path) == "" {
		return orgreports.WalletRollupRequest{}, op.NewValidationError("--input is required: pass the wallet rollup JSON request as a file path, or - to read it from stdin. Nothing was submitted.")
	}
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(io.LimitReader(stdin, 4<<20))
	} else {
		data, err = op.RuntimeFrom(ctx).Files.ReadFile(path)
	}
	if err != nil {
		return orgreports.WalletRollupRequest{}, err
	}
	return parseWalletRollupRequest(data)
}

// parseWalletRollupRequest checks a request against the platform's
// UpsertWalletRollupRequestDTO and RollupRule rules before anything is
// previewed or sent. Every refusal is a *op.ValidationError naming all the
// problems at once; messages name fields and allowed values, never submitted
// values. Unknown fields are refused rather than silently dropped. A null is
// treated as an omitted optional field, so a rule copied back from rollup get
// passes.
func parseWalletRollupRequest(data []byte) (orgreports.WalletRollupRequest, error) {
	var request orgreports.WalletRollupRequest
	if len(bytes.TrimSpace(data)) == 0 {
		return request, op.NewValidationError("the wallet rollup request is empty; see bitwave org wallets rollup set --help for the body. Nothing was submitted.")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var raw any
	if decoder.Decode(&raw) != nil || decoder.More() {
		return request, op.NewValidationError("the wallet rollup request is not valid JSON. Nothing was submitted.")
	}
	body, ok := raw.(map[string]any)
	if !ok {
		return request, op.NewValidationError("the wallet rollup request must be a JSON object with address, type and rules. Nothing was submitted.")
	}
	if problems := walletRollupProblems(body); len(problems) > 0 {
		return request, op.NewValidationError("invalid wallet rollup request: " + strings.Join(problems, "; ") + "; see bitwave org wallets rollup set --help. Nothing was submitted.")
	}
	if err := json.Unmarshal(data, &request); err != nil {
		return request, op.NewValidationError("the wallet rollup request could not be read. Nothing was submitted.")
	}
	// The platform runs a rule without cadence hourly but its upsert requires
	// the field, so a rule read back without one is sent as hour, as the web
	// app does.
	for i := range request.Rules {
		if request.Rules[i].Cadence == "" {
			request.Rules[i].Cadence = defaultWalletRollupCadence
		}
	}
	return request, nil
}

var (
	walletRollupTypes           = []string{"rollup-by-time", "wallet-rollup-by-time"}
	walletRollupFingerPrints    = []string{"simpleOutgoing", "simpleIncoming", "simpleTrade", "outgoingNative", "outgoingToken", "incomingNative", "incomingToken", "onlyFee", "errored", "any"}
	walletRollupActions         = []string{"rollupFromTo", "rollup", "rollupByAsset", "nonRollup", "ignore"}
	walletRollupCadences        = []string{"hour", "day", "month"}
	defaultWalletRollupCadence  = "hour"
	walletRollupTradeSeparation = []string{"none", "direction", "assets"}
	walletRollupRoundPeriods    = []string{"start-of-period", "end-of-period"}
	walletRollupClassification  = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

type rollupFieldKind int

const (
	rollupText rollupFieldKind = iota
	rollupBool
	rollupSeconds
	rollupTextList
	rollupTextMap
)

type rollupField struct {
	kind     rollupFieldKind
	required bool
	enum     []string
}

var walletRollupRuleFields = map[string]rollupField{
	"ruleName":                   {kind: rollupText, required: true},
	"classification":             {kind: rollupText, required: true},
	"fingerPrint":                {kind: rollupText, required: true, enum: walletRollupFingerPrints},
	"rollupAction":               {kind: rollupText, required: true, enum: walletRollupActions},
	"cadence":                    {kind: rollupText, enum: walletRollupCadences},
	"metadata":                   {kind: rollupTextMap},
	"involvedAccounts":           {kind: rollupTextList},
	"counterParties":             {kind: rollupTextList},
	"counterPartyExcludeFees":    {kind: rollupBool},
	"involvedWalletsExcludeFees": {kind: rollupBool},
	"separateByInvolvedAccounts": {kind: rollupBool},
	"separateByCounterParty":     {kind: rollupBool},
	"separateByTrade":            {kind: rollupText, enum: walletRollupTradeSeparation},
	"roundPeriod":                {kind: rollupText, enum: walletRollupRoundPeriods},
	"startSec":                   {kind: rollupSeconds},
	"endSec":                     {kind: rollupSeconds},
}

func walletRollupProblems(body map[string]any) []string {
	var problems []string
	for _, key := range rollupKeys(body) {
		if key != "address" && key != "type" && key != "rules" {
			problems = append(problems, fmt.Sprintf("%q is not a supported field (use address, type and rules)", safeRollupKey(key)))
		}
	}
	if address, present := body["address"]; present && address != nil {
		if text, ok := address.(string); !ok || strings.TrimSpace(text) == "" {
			problems = append(problems, "address must be the wallet's address as a non-empty string, or left out to use the wallet's own")
		}
	}
	if _, pasted := body["rollupRules"]; pasted {
		problems = append(problems, "this looks like rollup get's rollup object: send its rollupRules as rules, use type rollup-by-time, and drop walletId")
	}
	if kind, present := body["type"]; present && kind != nil {
		if text, ok := kind.(string); !ok || !slices.Contains(walletRollupTypes, text) {
			problems = append(problems, "type must be one of: "+strings.Join(walletRollupTypes, ", "))
		}
	} else {
		problems = append(problems, "type is required (rollup-by-time)")
	}
	rules, present := body["rules"]
	list, isList := rules.([]any)
	switch {
	case !present || rules == nil:
		problems = append(problems, "rules is required")
	case !isList:
		problems = append(problems, "rules must be a JSON array of rule objects")
	case len(list) == 0:
		problems = append(problems, "rules must hold at least one rule")
	default:
		for i, item := range list {
			problems = append(problems, walletRollupRuleProblems(fmt.Sprintf("rules[%d]", i), item)...)
		}
	}
	return problems
}

// bindWalletRollupAddress fills an omitted address with the wallet's own, and
// refuses one that is not among the wallet's addresses. The platform matches
// a rule's transactions against the configuration's address, so another
// wallet's address would save and never combine anything.
func bindWalletRollupAddress(request *orgreports.WalletRollupRequest, wallet *orgreports.OrgWallet) error {
	var addresses []string
	for _, address := range append([]string{wallet.Address}, wallet.Addresses...) {
		if address = strings.TrimSpace(address); address != "" {
			addresses = append(addresses, address)
		}
	}
	if len(addresses) == 0 {
		return op.NewValidationError("this wallet has no address on record, so it cannot have a Babel rollup. Nothing was submitted.")
	}
	given := strings.TrimSpace(request.Address)
	if given == "" {
		request.Address = addresses[0]
		return nil
	}
	for _, address := range addresses {
		if strings.EqualFold(address, given) {
			// Send the wallet's own spelling: the platform compares it with
			// transaction addresses as stored.
			request.Address = address
			return nil
		}
	}
	return op.NewValidationError("address is not one of this wallet's addresses. Rules only match transactions to or from the configured address, so this would save and never combine anything. Leave address out to use the wallet's own, or copy it from rollup get's wallet.address. Nothing was submitted.")
}

func walletRollupRuleProblems(at string, item any) []string {
	rule, ok := item.(map[string]any)
	if !ok {
		return []string{at + " must be a JSON object"}
	}
	var problems []string
	for _, key := range rollupKeys(rule) {
		if _, known := walletRollupRuleFields[key]; !known {
			problems = append(problems, fmt.Sprintf("%s: %q is not a supported field", at, safeRollupKey(key)))
		}
	}
	for _, name := range rollupKeys(walletRollupRuleFields) {
		field := walletRollupRuleFields[name]
		value, present := rule[name]
		if !present || value == nil {
			if field.required {
				problems = append(problems, at+"."+name+" is required")
			}
			continue
		}
		if problem := walletRollupValueProblem(field, value); problem != "" {
			problems = append(problems, at+"."+name+" "+problem)
		}
	}
	if text, ok := rule["classification"].(string); ok && strings.TrimSpace(text) != "" && !walletRollupClassification.MatchString(text) {
		problems = append(problems, at+".classification may contain only letters, digits, - and _")
	}
	if rule["involvedWalletsExcludeFees"] == true && rule["rollupAction"] != "rollup" {
		problems = append(problems, at+".involvedWalletsExcludeFees is only supported when rollupAction is rollup")
	}
	return problems
}

func walletRollupValueProblem(field rollupField, value any) string {
	switch field.kind {
	case rollupBool:
		if _, ok := value.(bool); !ok {
			return "must be a JSON boolean"
		}
	case rollupSeconds:
		number, ok := value.(json.Number)
		if !ok {
			return "must be a JSON integer (Unix seconds)"
		}
		if _, err := number.Int64(); err != nil {
			return "must be a JSON integer (Unix seconds)"
		}
	case rollupTextList:
		list, ok := value.([]any)
		if !ok {
			return "must be a JSON array of strings"
		}
		for _, entry := range list {
			if _, ok := entry.(string); !ok {
				return "must be a JSON array of strings"
			}
		}
	case rollupTextMap:
		object, ok := value.(map[string]any)
		if !ok {
			return "must be a JSON object of string values"
		}
		for _, entry := range object {
			if _, ok := entry.(string); !ok {
				return "must be a JSON object of string values"
			}
		}
	default:
		text, ok := value.(string)
		switch {
		case !ok:
			return "must be a string"
		case len(field.enum) > 0 && !slices.Contains(field.enum, text):
			return "must be one of: " + strings.Join(field.enum, ", ")
		case strings.TrimSpace(text) == "":
			return "must not be empty"
		}
	}
	return ""
}

func rollupKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// safeRollupKey keeps a caller-supplied field name to a plain alphabet so a
// refusal can name it without echoing arbitrary text.
func safeRollupKey(key string) string {
	var b strings.Builder
	for _, r := range key {
		if b.Len() >= 48 {
			b.WriteString("...")
			break
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('?')
		}
	}
	return b.String()
}

// walletRollupSetHelp is also the hosted tool description, so it carries the
// whole request body.
const walletRollupSetHelp = `Create or replace a wallet's Babel rollup configuration. This replaces the
wallet's whole rule list, so run bitwave org wallets rollup get first and send
every rule you want to keep. Pass the request as a file with --input, or with
--input - on stdin; --dry-run prints it without sending.

To resend what rollup get printed: send its rollup.rollupRules as rules, take
address from its wallet.address (or leave address out), use type
rollup-by-time, and drop walletId. The rollup object itself is not a request.

Request (a daily roll-up of plain sends):

  {"type": "rollup-by-time",
   "rules": [{"ruleName": "Daily sends", "classification": "daily-sends",
              "fingerPrint": "simpleOutgoing", "rollupAction": "rollupFromTo",
              "cadence": "day"}]}

address is optional. Left out, it is the wallet's own address; given, it must
be one of the wallet's addresses, because rules only match transactions to or
from it.

Rule fields:
  ruleName        display name (required)
  classification  short label of letters, digits, - and _; use a different
                  one for each rule (required)
  fingerPrint     which transactions the rule matches: simpleOutgoing or
                  simpleIncoming (plain sends or receives), simpleTrade,
                  outgoingNative, outgoingToken, incomingNative, incomingToken,
                  onlyFee, errored or any (required)
  rollupAction    rollupFromTo (one transaction per period, keeping from/to
                  addresses), rollup, rollupByAsset, nonRollup or ignore
                  (required)
  cadence         day, hour or month; omitted or null means hour, which is
                  how the platform runs a rule saved without one
  involvedAccounts, counterParties             lists of addresses
  separateByInvolvedAccounts, separateByCounterParty, counterPartyExcludeFees
                                               booleans
  involvedWalletsExcludeFees  boolean; only with rollupAction rollup
  separateByTrade  none, direction or assets
  roundPeriod      start-of-period or end-of-period
  startSec, endSec Unix seconds bounding the transactions the rule applies to
  metadata         object of string values

The request is checked before anything is sent, and unknown fields are
refused. A null counts as an omitted optional field.`

func resolveOrganizationWallet(cmd *op.Call, client *orgreports.Client, orgID, value string) (*orgreports.OrgWallet, error) {
	wallets, err := client.OrgWallets(cmd.Context(), orgID)
	if err != nil {
		return nil, fmt.Errorf("list organization wallets: %w", err)
	}
	for i := range wallets {
		if wallets[i].ID == value || strings.EqualFold(wallets[i].Name, value) {
			return &wallets[i], nil
		}
	}
	return nil, fmt.Errorf("wallet %q was not found", value)
}
