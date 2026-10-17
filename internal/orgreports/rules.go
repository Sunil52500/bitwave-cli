package orgreports

import (
	"context"
	"encoding/json"
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

const runRulesMutation = `mutation RunRulesForOrg($orgId: ID!) {
  runRulesForOrg(orgId: $orgId)
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

func (c *Client) RunRules(ctx context.Context, orgID string) error {
	request := map[string]any{
		"operationName": "RunRulesForOrg",
		"query":         runRulesMutation,
		"variables":     map[string]any{"orgId": orgID},
	}
	data, err := c.doEndpoint(ctx, http.MethodPost, c.RulesMutationURL, request, true)
	if err != nil {
		return err
	}
	var response struct {
		Data struct {
			Success bool `json:"runRulesForOrg"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return fmt.Errorf("decode run rules response: %w", err)
	}
	if err := graphqlErrors(response.Errors); err != nil {
		return err
	}
	if !response.Data.Success {
		return fmt.Errorf("run rules request was rejected")
	}
	return nil
}

func (c *Client) ExecuteBulkRules(ctx context.Context, orgID string, after, before int64) error {
	if after <= 0 || before <= 0 || after > before {
		return fmt.Errorf("invalid bulk rules date range: after=%d before=%d", after, before)
	}
	_, err := c.doEndpoint(ctx, http.MethodPost, c.BaseURL+"/org/"+url.PathEscape(orgID)+"/rules/execute", map[string]any{
		"executeUpdates": "true",
		"after":          after,
		"before":         before,
	}, true)
	return err
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
