package orgreports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/bitwave-io/bitwave-cli/internal/apierr"
)

const ruleFields = `
    id name disabled type priority methodId
    action {
      ... on DetailedCategorizationAction {
        lines { valueExtractor assetExtractor lineQualifierExtractor contactId categoryId metadataIds __typename }
        type ignoreFailPricing isIntercompanyTransfer __typename
      }
      ... on IgnoreAction { type __typename }
      ... on SimpleCategorizationAction { type contactId categoryId feeContactId feeCategoryId ignoreFailPricing __typename }
      ... on InternalTransferCategorizationAction {
        type internalFeeContactId: feeContactId internalFeeCategoryId: feeCategoryId ignoreFailPricing __typename
      }
      ... on SimpleSplitCategorizationAction {
        type
        splits { ... on PercentageSplit { percentage contactId categoryId __typename } __typename }
        feeSplits { ... on PercentageSplit { percentage contactId categoryId __typename } __typename }
        __typename
      }
      ... on TradeCategorizationAction { type tradeFeeContactId: feeContactId ignoreFailPricing __typename }
      ... on DeFiCategorizationAction { type deFiFeeContactId: feeContactId deFiWalletId __typename }
      ... on IntercompanyTransferCategorizationAction {
        type intercompanyFeeContactId: feeContactId intercompanyFeeCategoryId: feeCategoryId
        disposedCategoryId disposedContactId acquiredCategoryId acquiredContactId ignoreFailPricing __typename
      }
      __typename
    }
    coin description memo fromAddress toAddress
    valueRules { comparison value }
    afterDateSEC beforeDateSEC walletId direction autoReconcile collapseValues
    autoCategorizeFee multiToken accountingConnectionId includesCurrency allowMismatch
    metadataRule { operator metadata { key value __typename } txnRecordRule __typename }
    __typename
`

const rulesQuery = `query rules($orgId: ID!) {
  rules(orgId: $orgId) {` + ruleFields + `}
}`

const ruleQuery = `query rule($orgId: ID!, $ruleId: ID!) {
  rule(orgId: $orgId, ruleId: $ruleId) {` + ruleFields + `}
}`

const rulesPageQuery = `query rulesPaginated($orgId: ID!, $pageLimit: Int, $paginationToken: String) {
  rulesPaginated(orgId: $orgId, pageLimit: $pageLimit, paginationTokenOpt: $paginationToken) {
    items {` + ruleFields + `}
    nextPageToken
  }
}`

const createRuleMutation = `mutation CreateRule($orgId: ID!, $rule: Rule!) {
  createRule(orgId: $orgId, rule: $rule) { success errors }
}`

const updateRuleMutation = `mutation UpdateRule($orgId: ID!, $ruleId: ID!, $rule: Rule!) {
  updateRule(orgId: $orgId, ruleId: $ruleId, rule: $rule) { success errors }
}`

const toggleRuleMutation = `mutation ToggleRuleStatus($orgId: ID!, $ruleId: ID!, $disabled: Boolean!) {
  toggleRuleStatus(orgId: $orgId, ruleId: $ruleId, disabled: $disabled)
}`

const deleteRuleMutation = `mutation DeleteRule($orgId: ID!, $ruleId: ID!) {
  deleteRule(orgId: $orgId, ruleId: $ruleId)
}`

type RuleCreateResult struct {
	Success bool            `json:"success"`
	Errors  json.RawMessage `json:"errors,omitempty"`
}

type RulesPage struct {
	Items         []json.RawMessage `json:"items"`
	NextPageToken string            `json:"nextPageToken,omitempty"`
}

type graphQLError struct {
	Message string `json:"message"`
}

func (c *Client) Rules(ctx context.Context, orgID string) ([]json.RawMessage, error) {
	request := map[string]any{
		"operationName": "rules",
		"query":         rulesQuery,
		"variables":     map[string]any{"orgId": orgID},
	}
	data, err := c.doEndpoint(ctx, http.MethodPost, c.RulesQueryURL, request, true)
	if err != nil {
		return nil, err
	}
	var response struct {
		Data struct {
			Rules []json.RawMessage `json:"rules"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode rules response: %w", err)
	}
	if err := graphqlErrors(response.Errors); err != nil {
		return nil, err
	}
	return response.Data.Rules, nil
}

func (c *Client) Rule(ctx context.Context, orgID, ruleID string) (json.RawMessage, error) {
	request := map[string]any{
		"operationName": "rule", "query": ruleQuery,
		"variables": map[string]any{"orgId": orgID, "ruleId": ruleID},
	}
	data, err := c.doEndpoint(ctx, http.MethodPost, c.RulesQueryURL, request, true)
	if err != nil {
		return nil, err
	}
	var response struct {
		Data struct {
			Rule json.RawMessage `json:"rule"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode rule response: %w", err)
	}
	if err := graphqlErrors(response.Errors); err != nil {
		return nil, err
	}
	if len(response.Data.Rule) == 0 || string(response.Data.Rule) == "null" {
		return nil, fmt.Errorf("rule %q was not found", ruleID)
	}
	return response.Data.Rule, nil
}

func (c *Client) RulesPage(ctx context.Context, orgID string, pageLimit int, paginationToken string) (*RulesPage, error) {
	request := map[string]any{
		"operationName": "rulesPaginated", "query": rulesPageQuery,
		"variables": map[string]any{"orgId": orgID, "pageLimit": pageLimit, "paginationToken": paginationToken},
	}
	data, err := c.doEndpoint(ctx, http.MethodPost, c.RulesQueryURL, request, true)
	if err != nil {
		return nil, err
	}
	var response struct {
		Data struct {
			Page RulesPage `json:"rulesPaginated"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode paginated rules response: %w", err)
	}
	if err := graphqlErrors(response.Errors); err != nil {
		return nil, err
	}
	return &response.Data.Page, nil
}

func (c *Client) CreateRule(ctx context.Context, orgID string, rule json.RawMessage) (*RuleCreateResult, error) {
	request := map[string]any{
		"operationName": "CreateRule",
		"query":         createRuleMutation,
		"variables":     map[string]any{"orgId": orgID, "rule": rule},
	}
	data, err := c.doEndpoint(ctx, http.MethodPost, c.RulesMutationURL, request, true)
	if err != nil {
		return nil, err
	}
	var response struct {
		Data struct {
			CreateRule RuleCreateResult `json:"createRule"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode create rule response: %w", err)
	}
	if err := graphqlErrors(response.Errors); err != nil {
		return nil, err
	}
	if !response.Data.CreateRule.Success {
		return &response.Data.CreateRule, fmt.Errorf("create rule was rejected: %s", strings.TrimSpace(string(response.Data.CreateRule.Errors)))
	}
	return &response.Data.CreateRule, nil
}

func (c *Client) UpdateRule(ctx context.Context, orgID, ruleID string, rule json.RawMessage) (*RuleCreateResult, error) {
	request := map[string]any{
		"operationName": "UpdateRule",
		"query":         updateRuleMutation,
		"variables":     map[string]any{"orgId": orgID, "ruleId": ruleID, "rule": rule},
	}
	data, err := c.doEndpoint(ctx, http.MethodPost, c.RulesMutationURL, request, true)
	if err != nil {
		return nil, err
	}
	var response struct {
		Data struct {
			UpdateRule RuleCreateResult `json:"updateRule"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode update rule response: %w", err)
	}
	if err := graphqlErrors(response.Errors); err != nil {
		return nil, err
	}
	if !response.Data.UpdateRule.Success {
		return &response.Data.UpdateRule, fmt.Errorf("update rule was rejected: %s", strings.TrimSpace(string(response.Data.UpdateRule.Errors)))
	}
	return &response.Data.UpdateRule, nil
}

// RulesRunStart is sync-coordinator's answer to invoking the organization's
// rules-workflow job: the run it started, not its outcome.
type RulesRunStart struct {
	JobRunID string `json:"jobRunId"`
	JobID    string `json:"jobId"`
	JobName  string `json:"jobName,omitempty"`
	Message  string `json:"message,omitempty"`
}

// RulesRunInvokePath is the sync-coordinator route that starts an
// organization's standard rules run: the api-svc-rules runner's
// rules-workflow job, whose id is deterministic ({orgId}.rules-workflow), so
// no job lookup is needed. It is the route the app's "Run rules" button uses.
// overrideHeartbeat=true cancels an in-flight run for the job and bypasses
// the heartbeat guard, so an explicit request starts a run even shortly
// after the last one finished (the web apps send the same).
//
// The GraphQL runRulesForOrg mutation is deliberately not used: it is served
// by whichever api-svc the GraphQL route reaches, and on some hosts that is a
// legacy api-svc with the rules engine disabled, which accepts the mutation
// and never runs anything.
func RulesRunInvokePath(orgID string) string {
	return "/v3/orgs/" + url.PathEscape(orgID) + "/jobs/api-svc-rules/rules-workflow/" + url.PathEscape(orgID+".rules-workflow") + "/invoke?overrideHeartbeat=true"
}

// StartRulesRun asks sync-coordinator to start the organization's rules run
// and returns the job run it started. The run itself is asynchronous.
func (c *Client) StartRulesRun(ctx context.Context, orgID string) (*RulesRunStart, error) {
	var started RulesRunStart
	if err := c.doJSON(ctx, http.MethodPost, RulesRunInvokePath(orgID), map[string]any{}, &started); err != nil {
		return nil, err
	}
	if started.JobRunID == "" {
		return nil, fmt.Errorf("rules run response did not include a job run id")
	}
	return &started, nil
}

// BulkRulesNotEnabledDetail replaces the bulk-run endpoint's 403
// feature_not_enabled detail. It is static so hosted adapters can publish it
// verbatim, and it names the way out: the standard rules run needs no flag.
const BulkRulesNotEnabledDetail = "Bulk Run is not available because parallel rule processing is not enabled for this organization; use rule run (the standard rules run) instead"

func (c *Client) ExecuteBulkRules(ctx context.Context, orgID string, after, before int64) error {
	if after <= 0 || before <= 0 || after > before {
		return fmt.Errorf("invalid bulk rules date range: after=%d before=%d", after, before)
	}
	_, err := c.doEndpoint(ctx, http.MethodPost, c.BaseURL+"/org/"+url.PathEscape(orgID)+"/rules/execute", map[string]any{
		"executeUpdates": "true",
		"after":          after,
		"before":         before,
	}, true)
	return explainBulkRulesRefusal(err)
}

// explainBulkRulesRefusal turns the bulk-run endpoint's 403 for an
// organization without the parallel-rule-processing flag (api-svc answers
// code feature_not_enabled, message "Parallel rule processing is not enabled
// for this organization") into BulkRulesNotEnabledDetail. Every other error
// is returned unchanged.
func explainBulkRulesRefusal(err error) error {
	var apiError *apierr.Error
	if !errors.As(err, &apiError) || apiError.Status != http.StatusForbidden || !strings.Contains(strings.ToLower(apiError.Detail), "parallel rule processing") {
		return err
	}
	return &apierr.Error{Status: apiError.Status, Method: apiError.Method, URL: apiError.URL, Detail: BulkRulesNotEnabledDetail}
}

func (c *Client) ToggleRule(ctx context.Context, orgID, ruleID string, disabled bool) error {
	request := map[string]any{
		"operationName": "ToggleRuleStatus", "query": toggleRuleMutation,
		"variables": map[string]any{"orgId": orgID, "ruleId": ruleID, "disabled": disabled},
	}
	data, err := c.doEndpoint(ctx, http.MethodPost, c.RulesMutationURL, request, true)
	if err != nil {
		return err
	}
	var response struct {
		Data struct {
			Success bool `json:"toggleRuleStatus"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return fmt.Errorf("decode toggle rule response: %w", err)
	}
	if err := graphqlErrors(response.Errors); err != nil {
		return err
	}
	if !response.Data.Success {
		return fmt.Errorf("toggle rule was rejected")
	}
	return nil
}

func (c *Client) DeleteRule(ctx context.Context, orgID, ruleID string) error {
	request := map[string]any{
		"operationName": "DeleteRule", "query": deleteRuleMutation,
		"variables": map[string]any{"orgId": orgID, "ruleId": ruleID},
	}
	data, err := c.doEndpoint(ctx, http.MethodPost, c.RulesMutationURL, request, true)
	if err != nil {
		return err
	}
	var response struct {
		Data struct {
			Success bool `json:"deleteRule"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return fmt.Errorf("decode delete rule response: %w", err)
	}
	if err := graphqlErrors(response.Errors); err != nil {
		return err
	}
	if !response.Data.Success {
		return fmt.Errorf("delete rule was rejected")
	}
	return nil
}

func (c *Client) ValidateRule(ctx context.Context, orgID, transactionID, ruleID string) (json.RawMessage, error) {
	path := "/orgs/" + url.PathEscape(orgID) + "/transactions/" + url.PathEscape(transactionID) + "/rules/" + url.PathEscape(ruleID)
	data, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("rule validation response was not valid JSON")
	}
	return json.RawMessage(data), nil
}

func graphqlErrors(items []graphQLError) error {
	if len(items) == 0 {
		return nil
	}
	messages := make([]string, 0, len(items))
	for _, item := range items {
		messages = append(messages, item.Message)
	}
	return &apierr.GraphQLError{Messages: messages}
}
