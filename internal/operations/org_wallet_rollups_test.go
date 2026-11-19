package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	op "github.com/bitwave-io/bitwave-cli/internal/operation"
)

const rollupTestAddress = "0xfc488ae9cb395b150574aa5ce8a321c9100b1ee3"

const dailySendsRollup = `{"address":"` + rollupTestAddress + `","type":"rollup-by-time","rules":[` +
	`{"ruleName":"Daily sends","classification":"daily-sends","fingerPrint":"simpleOutgoing","rollupAction":"rollupFromTo","cadence":"day"}]}`

// fakeRollupAPI is a concrete fake of the Bitwave platform endpoints that
// `org wallets rollup set` calls: the OrgWallets query holds one wallet, and
// every rollup upsert body is recorded.
type fakeRollupAPI struct {
	wallet  string // the OrgWallets entry for w-1; the briber wallet when empty
	upserts []map[string]any
}

func (f *fakeRollupAPI) transport(t *testing.T) http.RoundTripper {
	return businessTransport(func(r *http.Request) (*http.Response, error) {
		reply := func(status int, body string) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: make(http.Header), Request: r, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		raw, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/graphql" && bytes.Contains(raw, []byte("OrgWallets")):
			wallet := f.wallet
			if wallet == "" {
				wallet = `{"id":"w-1","name":"Briber Wallet 1","address":"` + rollupTestAddress + `"}`
			}
			return reply(http.StatusOK, `{"data":{"wallets":[`+wallet+`]}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/orgs/org-1/wallets/w-1/rollup":
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				return nil, err
			}
			f.upserts = append(f.upserts, body)
			return reply(http.StatusNoContent, "")
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		return nil, nil
	})
}

func runRollupSet(t *testing.T, api *fakeRollupAPI, stdin string, args ...string) (string, error) {
	t.Helper()
	runtime, err := op.NewRuntime(op.Options{
		WorkingDirectory: t.TempDir(), OrganizationID: "org-1", Token: "token",
		CoreBaseURL: "https://unit.invalid", AppBaseURL: "https://unit.invalid",
		HTTPClient: &http.Client{Transport: api.transport(t)},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	definition := newOrgWalletRollupSetCmd()
	var output bytes.Buffer
	call := op.NewCall(op.WithRuntime(context.Background(), runtime), definition, strings.NewReader(stdin), &output, io.Discard)
	if err := definition.Flags().Parse(append(args, "--json")); err != nil {
		t.Fatal(err)
	}
	err = definition.Invoke(call, []string{"w-1"})
	return output.String(), err
}

func requireRollupValidation(t *testing.T, err error, wants ...string) {
	t.Helper()
	var validation *op.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("err = %#v (%v), want a validation error", err, err)
	}
	for _, want := range wants {
		if !strings.Contains(validation.Error(), want) {
			t.Fatalf("validation %q does not mention %q", validation.Error(), want)
		}
	}
}

func TestRollupSetSendsTheDocumentedBody(t *testing.T) {
	api := &fakeRollupAPI{}
	out, err := runRollupSet(t, api, dailySendsRollup, "--input", "-", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if len(api.upserts) != 1 {
		t.Fatalf("upserts = %#v", api.upserts)
	}
	var want map[string]any
	_ = json.Unmarshal([]byte(dailySendsRollup), &want)
	got, _ := json.Marshal(api.upserts[0])
	expected, _ := json.Marshal(want)
	if string(got) != string(expected) {
		t.Fatalf("sent %s, want %s", got, expected)
	}
	if !strings.Contains(out, `"status": "success"`) {
		t.Fatalf("out = %s", out)
	}
}

// Every local refusal must be a ValidationError: hosted adapters show only
// those to the caller, and report anything else as an unknown outcome.
func TestRollupSetLocalRefusalsAreValidationErrors(t *testing.T) {
	rule := func(extra string) string {
		return `{"address":"0xabc","type":"rollup-by-time","rules":[{"ruleName":"Sends","classification":"sends","fingerPrint":"any","rollupAction":"rollup","cadence":"day"` + extra + `}]}`
	}
	for name, tc := range map[string]struct {
		stdin string
		args  []string
		wants []string
	}{
		"no input flag":    {dailySendsRollup, []string{"--yes"}, []string{"--input is required"}},
		"empty stdin":      {"", []string{"--input", "-", "--yes"}, []string{"empty"}},
		"not json":         {"address: 0xabc", []string{"--input", "-", "--yes"}, []string{"not valid JSON"}},
		"not an object":    {`[1,2]`, []string{"--input", "-", "--yes"}, []string{"must be a JSON object"}},
		"guessed body":     {`{"direction":"outflow","period":"daily"}`, []string{"--input", "-", "--yes"}, []string{"type is required", "rules is required", `"direction" is not a supported field`}},
		"no rules":         {`{"address":"0xabc","type":"rollup-by-time","rules":[]}`, []string{"--input", "-", "--yes"}, []string{"rules must hold at least one rule"}},
		"bad type":         {`{"address":"0xabc","type":"babel","rules":[]}`, []string{"--input", "-", "--yes"}, []string{"type must be one of: rollup-by-time, wallet-rollup-by-time"}},
		"rule not object":  {`{"address":"0xabc","type":"rollup-by-time","rules":["daily"]}`, []string{"--input", "-", "--yes"}, []string{"rules[0] must be a JSON object"}},
		"bad enums":        {`{"address":"0xabc","type":"rollup-by-time","rules":[{"ruleName":"Sends","classification":"sends","fingerPrint":"outflow","rollupAction":"sum","cadence":"daily"}]}`, []string{"--input", "-", "--yes"}, []string{"rules[0].fingerPrint must be one of:", "rules[0].rollupAction must be one of:", "rules[0].cadence must be one of: hour, day, month"}},
		"missing rule key": {`{"address":"0xabc","type":"rollup-by-time","rules":[{"ruleName":"Sends"}]}`, []string{"--input", "-", "--yes"}, []string{"rules[0].classification is required", "rules[0].fingerPrint is required", "rules[0].rollupAction is required"}},
		"bad label":        {strings.Replace(rule(""), `"sends"`, `"daily sends"`, 1), []string{"--input", "-", "--yes"}, []string{"rules[0].classification may contain only letters, digits, - and _"}},
		"unknown rule key": {rule(`,"priority":1`), []string{"--input", "-", "--yes"}, []string{`rules[0]: "priority" is not a supported field`}},
		"seconds as text":  {rule(`,"startSec":"2026-01-01"`), []string{"--input", "-", "--yes"}, []string{"rules[0].startSec must be a JSON integer (Unix seconds)"}},
		"fee flag on from/to": {strings.Replace(rule(`,"involvedWalletsExcludeFees":true`), `"rollup"`, `"rollupFromTo"`, 1), []string{"--input", "-", "--yes"},
			[]string{"rules[0].involvedWalletsExcludeFees is only supported when rollupAction is rollup"}},
		"dry run checks too": {`{"period":"daily"}`, []string{"--input", "-", "--dry-run"}, []string{"type is required"}},
		"rollup get object": {`{"type":"wallet-rollup-by-time","walletId":"w-1","address":"0xabc","rollupRules":[{"ruleName":"Sends","classification":"sends","fingerPrint":"any","rollupAction":"rollup"}]}`, []string{"--input", "-", "--yes"},
			[]string{"rules is required", "send its rollupRules as rules", "drop walletId"}},
	} {
		t.Run(name, func(t *testing.T) {
			api := &fakeRollupAPI{}
			_, err := runRollupSet(t, api, tc.stdin, tc.args...)
			requireRollupValidation(t, err, tc.wants...)
			if len(api.upserts) != 0 {
				t.Fatalf("a refused request was sent: %#v", api.upserts)
			}
		})
	}
}

func TestRollupSetRefusalNeverEchoesSubmittedValues(t *testing.T) {
	_, err := runRollupSet(t, &fakeRollupAPI{}, `{"address":"0xabc","type":"see https://evil.test","rules":[{"ruleName":"x","classification":"<script>","fingerPrint":"token=secret","rollupAction":"rollup","cadence":"day","https://evil.test/x":1}]}`, "--input", "-", "--yes")
	requireRollupValidation(t, err, "type must be one of", "classification", "fingerPrint")
	for _, leaked := range []string{"evil.test", "<script>", "secret"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("validation echoes %q: %v", leaked, err)
		}
	}
}

// The help tells callers to read the rules and send back every one to keep;
// a rule as rollup get returns it (nulls for unset fields) must pass, and the
// fee flag must reach the platform instead of being dropped.
func TestRollupSetForwardsACopiedBackRule(t *testing.T) {
	api := &fakeRollupAPI{}
	body := `{"address":"` + rollupTestAddress + `","type":"wallet-rollup-by-time","rules":[{"ruleName":"Fees","classification":"fees","fingerPrint":"onlyFee",` +
		`"rollupAction":"rollup","cadence":"month","metadata":null,"involvedAccounts":["0x1"],"counterParties":null,"startSec":1767225600,"endSec":null,` +
		`"separateByInvolvedAccounts":false,"separateByCounterParty":null,"separateByTrade":"direction","roundPeriod":"start-of-period",` +
		`"counterPartyExcludeFees":null,"involvedWalletsExcludeFees":true}]}`
	if _, err := runRollupSet(t, api, body, "--input", "-", "--yes"); err != nil {
		t.Fatal(err)
	}
	sent := api.upserts[0]["rules"].([]any)[0].(map[string]any)
	if sent["involvedWalletsExcludeFees"] != true {
		t.Fatalf("involvedWalletsExcludeFees was dropped: %#v", sent)
	}
	if sent["startSec"] != float64(1767225600) || sent["separateByTrade"] != "direction" || sent["roundPeriod"] != "start-of-period" {
		t.Fatalf("rule = %#v", sent)
	}
}

func TestRollupSetHelpDocumentsTheBody(t *testing.T) {
	long := strings.Join(strings.Fields(newOrgWalletRollupSetCmd().Long), " ")
	for _, want := range []string{
		"replaces the wallet's whole rule list",
		"bitwave org wallets rollup get",
		`"type": "rollup-by-time"`,
		"simpleOutgoing", "rollupFromTo", "day, hour or month",
		"involvedWalletsExcludeFees",
		"send its rollup.rollupRules as rules", "take address from its wallet.address", "drop walletId",
		"address is optional", "must be one of the wallet's addresses",
	} {
		if !strings.Contains(long, want) {
			t.Fatalf("help does not mention %q:\n%s", want, long)
		}
	}
	// The worked example in the help must itself be a valid request.
	start := strings.Index(newOrgWalletRollupSetCmd().Long, "{")
	end := strings.LastIndex(newOrgWalletRollupSetCmd().Long, "}")
	if _, err := parseWalletRollupRequest([]byte(newOrgWalletRollupSetCmd().Long[start : end+1])); err != nil {
		t.Fatalf("the documented example is refused: %v", err)
	}
}

func TestRollupRuleFieldTypesAreChecked(t *testing.T) {
	base := `"ruleName":"Sends","classification":"sends","fingerPrint":"any","rollupAction":"rollup","cadence":"day"`
	for name, tc := range map[string]struct{ extra, want string }{
		"boolean as text":     {`"separateByCounterParty":"true"`, "rules[0].separateByCounterParty must be a JSON boolean"},
		"list as text":        {`"involvedAccounts":"0x1"`, "rules[0].involvedAccounts must be a JSON array of strings"},
		"list of numbers":     {`"counterParties":[1]`, "rules[0].counterParties must be a JSON array of strings"},
		"metadata as list":    {`"metadata":["a"]`, "rules[0].metadata must be a JSON object of string values"},
		"metadata number":     {`"metadata":{"a":1}`, "rules[0].metadata must be a JSON object of string values"},
		"seconds as fraction": {`"endSec":1.5`, "rules[0].endSec must be a JSON integer (Unix seconds)"},
		"blank name":          {`"ruleName":" "`, "rules[0].ruleName must not be empty"},
		"number as text":      {`"roundPeriod":5`, "rules[0].roundPeriod must be a string"},
	} {
		t.Run(name, func(t *testing.T) {
			body := `{"address":"0xabc","type":"rollup-by-time","rules":[{` + base + `,` + tc.extra + `}]}`
			if strings.Contains(tc.extra, `"ruleName"`) {
				body = strings.Replace(body, `"ruleName":"Sends",`, "", 1)
			}
			_, err := parseWalletRollupRequest([]byte(body))
			requireRollupValidation(t, err, tc.want)
		})
	}
}

func TestRollupRefusalShortensALongFieldName(t *testing.T) {
	_, err := parseWalletRollupRequest([]byte(`{"address":"0xabc","type":"rollup-by-time","rules":[{"ruleName":"x"}],"` + strings.Repeat("a", 80) + `":1}`))
	requireRollupValidation(t, err, `"`+strings.Repeat("a", 48)+`..." is not a supported field`, "address", "rules[0].fingerPrint is required")
	if strings.Contains(err.Error(), strings.Repeat("a", 49)) {
		t.Fatalf("long field name not shortened: %v", err)
	}
	_, err = parseWalletRollupRequest([]byte(`{"address":"","type":"rollup-by-time","rules":{}}`))
	requireRollupValidation(t, err, "address must be the wallet's address as a non-empty string", "rules must be a JSON array of rule objects")
}

// api-svc runs a stored rule without cadence hourly (rollupRule.cadence ??
// RollupPeriod.Hourly) but its upsert DTO requires the field. An old rule read
// back without one must still save, as hourly, the way web-app-v3 sends it.
func TestRollupSetDefaultsAMissingCadenceToHour(t *testing.T) {
	for name, cadence := range map[string]string{"omitted": "", "null": `,"cadence":null`} {
		t.Run(name, func(t *testing.T) {
			api := &fakeRollupAPI{}
			body := `{"address":"` + rollupTestAddress + `","type":"rollup-by-time","rules":[` +
				`{"ruleName":"Old","classification":"old","fingerPrint":"any","rollupAction":"rollup"` + cadence + `},` +
				`{"ruleName":"Daily sends","classification":"daily-sends","fingerPrint":"simpleOutgoing","rollupAction":"rollupFromTo","cadence":"day"}]}`
			if _, err := runRollupSet(t, api, body, "--input", "-", "--yes"); err != nil {
				t.Fatal(err)
			}
			rules := api.upserts[0]["rules"].([]any)
			if got := rules[0].(map[string]any)["cadence"]; got != "hour" {
				t.Fatalf("missing cadence sent as %#v, want hour", got)
			}
			if got := rules[1].(map[string]any)["cadence"]; got != "day" {
				t.Fatalf("explicit cadence sent as %#v, want day", got)
			}
		})
	}
}

// The platform matches a rule's transactions against the configuration's
// address, so another wallet's address saves and never combines anything. With
// five briber wallets that mix-up is easy; it must be refused before sending.
func TestRollupSetRefusesAnotherWalletsAddress(t *testing.T) {
	other := strings.Replace(dailySendsRollup, rollupTestAddress, "0x2222222222222222222222222222222222222222", 1)
	for name, args := range map[string][]string{"write": {"--input", "-", "--yes"}, "dry run": {"--input", "-", "--dry-run"}} {
		t.Run(name, func(t *testing.T) {
			api := &fakeRollupAPI{}
			_, err := runRollupSet(t, api, other, args...)
			requireRollupValidation(t, err, "address is not one of this wallet's addresses", "wallet.address", "Nothing was submitted")
			if strings.Contains(err.Error(), "0x2222") {
				t.Fatalf("validation echoes the submitted address: %v", err)
			}
			if len(api.upserts) != 0 {
				t.Fatalf("a refused request was sent: %#v", api.upserts)
			}
		})
	}
}

func TestRollupSetBindsTheWalletsAddress(t *testing.T) {
	second := "0x3333333333333333333333333333333333333333"
	for name, tc := range map[string]struct{ wallet, body, want string }{
		"omitted is filled": {"", strings.Replace(dailySendsRollup, `"address":"`+rollupTestAddress+`",`, "", 1), rollupTestAddress},
		"case differs":      {"", strings.Replace(dailySendsRollup, rollupTestAddress, strings.ToUpper(rollupTestAddress), 1), rollupTestAddress},
		"one of addresses": {`{"id":"w-1","name":"Briber Wallet 1","addresses":["` + rollupTestAddress + `","` + second + `"]}`,
			strings.Replace(dailySendsRollup, rollupTestAddress, second, 1), second},
		"omitted, addresses only": {`{"id":"w-1","name":"Briber Wallet 1","addresses":["` + second + `"]}`,
			strings.Replace(dailySendsRollup, `"address":"`+rollupTestAddress+`",`, "", 1), second},
	} {
		t.Run(name, func(t *testing.T) {
			api := &fakeRollupAPI{wallet: tc.wallet}
			if _, err := runRollupSet(t, api, tc.body, "--input", "-", "--yes"); err != nil {
				t.Fatal(err)
			}
			if got := api.upserts[0]["address"]; got != tc.want {
				t.Fatalf("sent address %#v, want %s", got, tc.want)
			}
		})
	}
}

func TestRollupSetRefusesAWalletWithoutAnAddress(t *testing.T) {
	api := &fakeRollupAPI{wallet: `{"id":"w-1","name":"Exchange account"}`}
	_, err := runRollupSet(t, api, dailySendsRollup, "--input", "-", "--yes")
	requireRollupValidation(t, err, "no address on record")
	if len(api.upserts) != 0 {
		t.Fatalf("a refused request was sent: %#v", api.upserts)
	}
}
