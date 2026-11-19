package orgreports

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

type BabelRollupRule struct {
	RuleName                   string            `json:"ruleName"`
	Classification             string            `json:"classification"`
	FingerPrint                string            `json:"fingerPrint"`
	RollupAction               string            `json:"rollupAction"`
	Cadence                    string            `json:"cadence"`
	Metadata                   map[string]string `json:"metadata,omitempty"`
	InvolvedAccounts           []string          `json:"involvedAccounts,omitempty"`
	CounterParties             []string          `json:"counterParties,omitempty"`
	CounterPartyExcludeFees    bool              `json:"counterPartyExcludeFees,omitempty"`
	InvolvedWalletsExcludeFees bool              `json:"involvedWalletsExcludeFees,omitempty"`
	StartSEC                   *int64            `json:"startSec,omitempty"`
	EndSEC                     *int64            `json:"endSec,omitempty"`
	SeparateByInvolvedAccounts bool              `json:"separateByInvolvedAccounts,omitempty"`
	SeparateByCounterParty     bool              `json:"separateByCounterParty,omitempty"`
	SeparateByTrade            string            `json:"separateByTrade,omitempty"`
	RoundPeriod                string            `json:"roundPeriod,omitempty"`
}

type WalletRollupRequest struct {
	Address string            `json:"address"`
	Type    string            `json:"type"`
	Rules   []BabelRollupRule `json:"rules"`
}

func (c *Client) UpsertWalletRollup(ctx context.Context, orgID, walletID string, input WalletRollupRequest) error {
	if len(input.Rules) == 0 {
		return fmt.Errorf("at least one Babel rollup rule is required")
	}
	path := "/orgs/" + url.PathEscape(orgID) + "/wallets/" + url.PathEscape(walletID) + "/rollup"
	_, err := c.do(ctx, http.MethodPost, path, input)
	return err
}

func (c *Client) WalletRollup(ctx context.Context, orgID, walletID string) (json.RawMessage, error) {
	path := "/orgs/" + url.PathEscape(orgID) + "/wallets/" + url.PathEscape(walletID) + "/rollup"
	data, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return json.RawMessage("null"), nil
	}
	return json.RawMessage(data), nil
}
