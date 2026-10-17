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

	"github.com/bitwave-io/bitwave-cli/internal/apierr"
	op "github.com/bitwave-io/bitwave-cli/internal/operation"
)

const testWalletAddress = "0xfc488ae9cb395b150574aa5ce8a321c9100b1ee3"

// fakeOrgAPI is a concrete fake of the Bitwave platform endpoints that
// `org wallets add` calls. It records every createWallet input and answers
// with createReply.
type fakeOrgAPI struct {
	existing     string
	subsidiaries string
	createReply  string
	created      []map[string]any
}

func (f *fakeOrgAPI) transport(t *testing.T) http.RoundTripper {
	return businessTransport(func(r *http.Request) (*http.Response, error) {
		reply := func(body string) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: r, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/orgs/org-1/wallets":
			return reply(f.existing)
		case r.Method == http.MethodGet && r.URL.Path == "/orgs/org-1/subsidiaries":
			return reply(f.subsidiaries)
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			var request struct {
				Variables struct {
					Wallet map[string]any `json:"wallet"`
				} `json:"variables"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				return nil, err
			}
			f.created = append(f.created, request.Variables.Wallet)
			return reply(f.createReply)
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		return nil, nil
	})
}

const createdWallet = `{"data":{"createWallet":{"id":"w-1","name":"x","networkId":"eth"}}}`

func newFakeOrgAPI() *fakeOrgAPI {
	return &fakeOrgAPI{existing: `{"items":[]}`, subsidiaries: `[]`, createReply: createdWallet}
}

func runWalletsAdd(t *testing.T, api *fakeOrgAPI, args ...string) (string, error) {
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
	definition := newOrgWalletsAddCmd()
	var output bytes.Buffer
	call := op.NewCall(op.WithRuntime(context.Background(), runtime), definition, nil, &output, io.Discard)
	if err := definition.Flags().Parse(append(args, "--json")); err != nil {
		t.Fatal(err)
	}
	err = definition.Invoke(call, nil)
	return output.String(), err
}

func TestWalletAddDefaultsAMissingName(t *testing.T) {
	api := newFakeOrgAPI()
	if out, err := runWalletsAdd(t, api, "--network", "ethereum", "--address", testWalletAddress, "--yes"); err != nil {
		t.Fatalf("%v (output: %s)", err, out)
	}
	if len(api.created) != 1 || api.created[0]["name"] != "Ethereum 0xfc48...1ee3" {
		t.Fatalf("created = %#v", api.created)
	}
}

func TestWalletAddDryRunWithoutNamePreviewsTheDefault(t *testing.T) {
	api := newFakeOrgAPI()
	out, err := runWalletsAdd(t, api, "--network", "polygon", "--address", testWalletAddress, "--dry-run")
	if err != nil {
		t.Fatalf("%v (output: %s)", err, out)
	}
	if !strings.Contains(out, `"name": "Polygon 0xfc48...1ee3"`) || len(api.created) != 0 {
		t.Fatalf("output = %s, created = %#v", out, api.created)
	}
}

func TestDefaultOrgWalletName(t *testing.T) {
	for _, tc := range []struct {
		input orgWalletInput
		want  string
	}{
		{orgWalletInput{NetworkID: "eth", Address: testWalletAddress}, "Ethereum 0xfc48...1ee3"},
		{orgWalletInput{NetworkID: "sol", Address: "short"}, "Solana short"},
		{orgWalletInput{NetworkID: "newchain", Address: testWalletAddress}, "NEWCHAIN 0xfc48...1ee3"},
		{orgWalletInput{NetworkID: "monad", Type: orgWalletTypeDefi, Address: testWalletAddress}, "MONAD DeFi 0xfc48...1ee3"},
	} {
		if got := defaultOrgWalletName(tc.input); got != tc.want {
			t.Errorf("defaultOrgWalletName(%#v) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestWalletAddKeepsAnExplicitName(t *testing.T) {
	api := newFakeOrgAPI()
	if _, err := runWalletsAdd(t, api, "--name", "Treasury", "--network", "eth", "--address", testWalletAddress, "--yes"); err != nil {
		t.Fatal(err)
	}
	if api.created[0]["name"] != "Treasury" {
		t.Fatalf("created = %#v", api.created)
	}
}

func TestWalletAddUsesTheOnlySubsidiary(t *testing.T) {
	api := newFakeOrgAPI()
	api.subsidiaries = `[{"id":"sub-root","name":"Root"}]`
	if _, err := runWalletsAdd(t, api, "--name", "Treasury", "--network", "eth", "--address", testWalletAddress, "--yes"); err != nil {
		t.Fatal(err)
	}
	if api.created[0]["subsidiaryId"] != "sub-root" {
		t.Fatalf("created = %#v", api.created)
	}
}

func TestWalletAddDoesNotPickAmongSeveralSubsidiaries(t *testing.T) {
	api := newFakeOrgAPI()
	api.subsidiaries = `[{"id":"sub-a","name":"A"},{"id":"sub-b","name":"B"}]`
	if _, err := runWalletsAdd(t, api, "--name", "Treasury", "--network", "eth", "--address", testWalletAddress, "--yes"); err != nil {
		t.Fatal(err)
	}
	if _, set := api.created[0]["subsidiaryId"]; set {
		t.Fatalf("created = %#v", api.created)
	}
}

func TestWalletAddCreatesWithoutSubsidiaryWhenTheListFails(t *testing.T) {
	// Orgs without subsidiaries must keep working even if the list errors.
	api := newFakeOrgAPI()
	api.subsidiaries = `not json`
	if _, err := runWalletsAdd(t, api, "--name", "Treasury", "--network", "eth", "--address", testWalletAddress, "--yes"); err != nil {
		t.Fatal(err)
	}
	if _, set := api.created[0]["subsidiaryId"]; set {
		t.Fatalf("created = %#v", api.created)
	}
}

func TestWalletAddRejectsAnUnknownSubsidiaryAsValidation(t *testing.T) {
	api := newFakeOrgAPI()
	api.subsidiaries = `[{"id":"sub-a","name":"A"}]`
	_, err := runWalletsAdd(t, api, "--name", "Treasury", "--network", "eth", "--address", testWalletAddress, "--subsidiary", "sub-9", "--yes")
	var validation *op.ValidationError
	if !errors.As(err, &validation) || validation.Error() != "subsidiary is not one of this organization's subsidiaries" {
		t.Fatalf("err = %#v", err)
	}
	if !strings.Contains(err.Error(), "sub-9") || len(api.created) != 0 {
		t.Fatalf("err = %v, created = %#v", err, api.created)
	}
}

func TestWalletAddLocalRefusalsAreValidationErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"missing address": {[]string{"--network", "eth", "--yes"}, "address is required"},
		"missing network": {[]string{"--address", testWalletAddress, "--yes"}, "networkId is required"},
		"missing yes":     {[]string{"--network", "eth", "--address", testWalletAddress}, "refusing to change the organization without --yes (use --dry-run to preview)"},
		"hd on eth":       {[]string{"--network", "eth", "--address", "xpub1", "--address-type", "hd", "--yes"}, "HD wallets are supported only for btc and dash"},
		"bad type":        {[]string{"--type", "vault", "--network", "eth", "--address", testWalletAddress, "--yes"}, "type must be blockchain or defi"},
		"bad concurrency": {[]string{"--network", "eth", "--address", testWalletAddress, "--concurrency", "0", "--yes"}, "--concurrency must be between 1 and 50"},
	} {
		t.Run(name, func(t *testing.T) {
			api := newFakeOrgAPI()
			_, err := runWalletsAdd(t, api, tc.args...)
			var validation *op.ValidationError
			if !errors.As(err, &validation) || validation.Error() != tc.want {
				t.Fatalf("err = %#v (%v), want validation %q", err, err, tc.want)
			}
			if len(api.created) != 0 {
				t.Fatalf("created = %#v", api.created)
			}
		})
	}
}

func TestWalletAddBackendRefusalIsReturnedTyped(t *testing.T) {
	api := newFakeOrgAPI()
	api.createReply = `{"data":{"createWallet":null},"errors":[{"message":"Wallet must have subsidiary specified for org org-1"}]}`
	out, err := runWalletsAdd(t, api, "--name", "Treasury", "--network", "eth", "--address", testWalletAddress, "--yes")
	var batch *op.BatchError
	if !errors.As(err, &batch) || batch.Created != 0 || batch.Skipped != 0 || len(batch.Failures) != 1 {
		t.Fatalf("err = %#v", err)
	}
	var refusal *apierr.GraphQLError
	if !errors.As(err, &refusal) || len(refusal.Messages) != 1 || refusal.Messages[0] != "Wallet must have subsidiary specified for org org-1" {
		t.Fatalf("err = %#v", err)
	}
	if err.Error() != "organization wallets: 0 created, 0 skipped, 1 failed; first failure: GraphQL: Wallet must have subsidiary specified for org org-1" {
		t.Fatalf("err = %q", err.Error())
	}
	if !strings.Contains(out, `"status": "partial_failure"`) {
		t.Fatalf("the JSON envelope is still written: %s", out)
	}
}

func TestWalletAddHelpDescribesNameAndSubsidiary(t *testing.T) {
	long := strings.Join(strings.Fields(newOrgWalletsAddCmd().Long), " ")
	for _, want := range []string{"defaults to the network name and a shortened address", "has only one subsidiary it is used", "bitwave org admin subsidiaries list", "--dry-run only prints the request"} {
		if !strings.Contains(long, want) {
			t.Errorf("Long help is missing %q:\n%s", want, long)
		}
	}
}
