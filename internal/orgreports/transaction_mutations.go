package orgreports

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	TransactionStateIgnore   = "ignore"
	TransactionStateUnignore = "un-ignore"

	TransactionReviewAccept = "accept-pending-changes"
	TransactionReviewIgnore = "ignore-pending-changes"
)

type BulkStateRequest struct {
	BulkActionID   string   `json:"bulkActionId,omitempty"`
	TransactionIDs []string `json:"transactionIds"`
	Update         string   `json:"update"`
}

type TransactionFailure struct {
	TransactionID string `json:"transactionId"`
	Error         string `json:"error"`
}

type BulkStateResponse struct {
	WorkflowID   string               `json:"workflowId,omitempty"`
	BulkActionID string               `json:"bulkActionId,omitempty"`
	Status       string               `json:"status,omitempty"`
	Success      bool                 `json:"success"`
	Processed    int                  `json:"processed"`
	SuccessCount int                  `json:"successCount"`
	Failed       []TransactionFailure `json:"failed"`
	Transactions []struct {
		TransactionID string `json:"transactionId"`
		Status        string `json:"status"`
		Error         string `json:"error,omitempty"`
	} `json:"transactions,omitempty"`
}

type TransactionReviewRequest struct {
	Transactions []TransactionReviewItem `json:"transactions"`
}

type TransactionReviewItem struct {
	TransactionID string `json:"transactionId"`
}

type BulkCategorizeResult struct {
	Success bool   `json:"success"`
	TxnID   string `json:"txnId"`
	Reason  string `json:"reason,omitempty"`
}

type Category struct {
	ID                     string `json:"id"`
	Name                   string `json:"name"`
	Code                   string `json:"code,omitempty"`
	Enabled                bool   `json:"enabled"`
	Source                 string `json:"source,omitempty"`
	Type                   string `json:"type,omitempty"`
	AccountingConnectionID string `json:"accountingConnectionId,omitempty"`
}

type Contact struct {
	ID                       string           `json:"id"`
	Name                     string           `json:"name"`
	RemoteID                 string           `json:"remoteId,omitempty"`
	FirstName                string           `json:"firstName,omitempty"`
	LastName                 string           `json:"lastName,omitempty"`
	EmailAddress             string           `json:"emailAddress,omitempty"`
	Enabled                  bool             `json:"enabled"`
	Source                   string           `json:"source,omitempty"`
	Type                     string           `json:"type,omitempty"`
	AccountingConnectionID   string           `json:"accountingConnectionId,omitempty"`
	DefaultExpenseCategoryID string           `json:"defaultExpenseCategoryId,omitempty"`
	DefaultRevenueCategoryID string           `json:"defaultRevenueCategoryId,omitempty"`
	Metadata                 map[string]any   `json:"metadata,omitempty"`
	Addresses                []ContactAddress `json:"addresses,omitempty"`
}

type ContactAddress struct {
	Coin                     string `json:"coin,omitempty"`
	Address                  string `json:"address,omitempty"`
	Memo                     string `json:"memo,omitempty"`
	NetworkID                string `json:"networkId,omitempty"`
	DefaultExpenseCategoryID string `json:"defaultExpenseCategoryId,omitempty"`
	DefaultRevenueCategoryID string `json:"defaultRevenueCategoryId,omitempty"`
}

type CreateContactInput struct {
	ConnectionID string `json:"connectionId"`
	RemoteID     string `json:"remoteId,omitempty"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	FirstName    string `json:"firstName,omitempty"`
	LastName     string `json:"lastName,omitempty"`
	EmailAddress string `json:"emailAddress,omitempty"`
}

type AccountingConnection struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Type     string `json:"type"`
	Disabled bool   `json:"disabled"`
}

type CreateManualAccountingConnectionResponse struct {
	ConnectionID string `json:"connectionId"`
}

// CreateChartAccountInput is Bitwave's category creation contract. In a manual
// accounting connection these categories are the organization chart of
// accounts used by categorization and rules.
type CreateChartAccountInput struct {
	ConnectionID string `json:"connectionId"`
	Source       string `json:"source"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Code         string `json:"code"`
	Description  string `json:"description"`
}

type CreateChartAccountResponse struct {
	ID string `json:"id"`
}

type TransactionSearchRequest struct {
	Timezone      string                   `json:"timezone,omitempty"`
	Limit         int                      `json:"limit"`
	NextToken     string                   `json:"nextToken,omitempty"`
	SortBy        string                   `json:"sortBy,omitempty"`
	SortDirection string                   `json:"sortDirection,omitempty"`
	Filters       TransactionExportFilters `json:"filters"`
}

type TransactionSearchResponse struct {
	Transactions []json.RawMessage `json:"transactions"`
	NextToken    string            `json:"nextToken,omitempty"`
	PrevToken    string            `json:"prevToken,omitempty"`
	AssetIDs     []string          `json:"assetIds,omitempty"`
}

type TransactionOverviewRequest struct {
	Timezone string                   `json:"timezone,omitempty"`
	Filters  TransactionExportFilters `json:"filters"`
}

type TransactionOverviewResponse struct {
	NeedsCategorization int     `json:"needsCategorization"`
	ToBeReconciled      int     `json:"toBeReconciled"`
	NeedsReview         int     `json:"needsReview"`
	All                 int     `json:"all"`
	FirstRecordDate     *string `json:"firstRecordDate"`
}

// CreateTransaction is the public transaction-ingest contract used by the
// Bitwave transaction UI. Numeric quantities remain strings to avoid losing
// precision in automation and LLM tool calls.
type CreateTransaction struct {
	SystemID               string         `json:"systemId"`
	Time                   string         `json:"time"`
	AccountID              string         `json:"accountId"`
	Amount                 string         `json:"amount"`
	AmountTicker           string         `json:"amountTicker"`
	TransactionType        string         `json:"transactionType"`
	TradeID                string         `json:"tradeId,omitempty"`
	GroupID                string         `json:"groupId,omitempty"`
	BlockchainID           string         `json:"blockchainId,omitempty"`
	Cost                   string         `json:"cost,omitempty"`
	CostTicker             string         `json:"costTicker,omitempty"`
	Fee                    string         `json:"fee,omitempty"`
	FeeTicker              string         `json:"feeTicker,omitempty"`
	CategoryID             string         `json:"categoryId,omitempty"`
	ContactID              string         `json:"contactId,omitempty"`
	AccountingConnectionID string         `json:"accountingConnectionId,omitempty"`
	Memo                   string         `json:"memo,omitempty"`
	Description            string         `json:"description,omitempty"`
	FromAddress            string         `json:"fromAddress,omitempty"`
	ToAddress              string         `json:"toAddress,omitempty"`
	Metadata               map[string]any `json:"metadata,omitempty"`
}

type InternalTransferInput struct {
	FromWalletID string `json:"fromWalletId"`
	ToWalletID   string `json:"toWalletId"`
	Coin         string `json:"coin"`
	Amount       string `json:"amount"`
	CreatedSEC   int64  `json:"createdSEC"`
	Memo         string `json:"memo,omitempty"`
}

func (c *Client) SearchTransactions(ctx context.Context, orgID string, input TransactionSearchRequest) (*TransactionSearchResponse, error) {
	var response TransactionSearchResponse
	path := "/v3/orgs/" + url.PathEscape(orgID) + "/transactions/search"
	if err := c.doJSON(ctx, http.MethodPost, path, input, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// TransactionIDsBody is the body the product UI sends to combine or uncombine.
type TransactionIDsBody struct {
	TxnIDs []string `json:"txnIds"`
}

// CombineTransactionsResponse carries the id of the combined parent.
type CombineTransactionsResponse struct {
	ID string `json:"id"`
}

// UncombineTransactionResult is one per-parent outcome of an uncombine.
type UncombineTransactionResult struct {
	TxID   string `json:"txId"`
	Status string `json:"status"`
}

// CombineTransactions merges the given transactions into one new parent and
// returns the parent's id. The server refuses reconciled, already-combined and
// closed-period transactions; the parent starts uncategorized.
func (c *Client) CombineTransactions(ctx context.Context, orgID string, ids []string) (*CombineTransactionsResponse, error) {
	var response CombineTransactionsResponse
	path := "/v3/orgs/" + url.PathEscape(orgID) + "/transactions/combine"
	if err := c.doJSON(ctx, http.MethodPut, path, TransactionIDsBody{TxnIDs: ids}, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// UncombineTransactions restores the children of each combined parent.
func (c *Client) UncombineTransactions(ctx context.Context, orgID string, ids []string) ([]UncombineTransactionResult, error) {
	var response []UncombineTransactionResult
	path := "/v3/orgs/" + url.PathEscape(orgID) + "/transactions/uncombine"
	if err := c.doJSON(ctx, http.MethodPut, path, TransactionIDsBody{TxnIDs: ids}, &response); err != nil {
		return nil, err
	}
	return response, nil
}

func (c *Client) TransactionCount(ctx context.Context, orgID string, input TransactionOverviewRequest) (int, error) {
	var response struct {
		Count int `json:"count"`
	}
	path := "/v3/orgs/" + url.PathEscape(orgID) + "/transactions/count"
	if err := c.doJSON(ctx, http.MethodPost, path, input, &response); err != nil {
		return 0, err
	}
	return response.Count, nil
}

func (c *Client) TransactionOverview(ctx context.Context, orgID string, input TransactionOverviewRequest) (*TransactionOverviewResponse, error) {
	var response TransactionOverviewResponse
	path := "/orgs/" + url.PathEscape(orgID) + "/transactions/summary_v2"
	if err := c.doJSON(ctx, http.MethodPost, path, input, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) CreateTransactions(ctx context.Context, orgID string, input []CreateTransaction) (json.RawMessage, error) {
	path := "/txns/orgs/" + url.PathEscape(orgID) + "/transactions?immediate=true"
	data, err := c.do(ctx, http.MethodPost, path, input)
	if err != nil {
		return nil, err
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("create transaction response was not valid JSON")
	}
	return json.RawMessage(data), nil
}

func (c *Client) CreateInternalTransfer(ctx context.Context, orgID string, input InternalTransferInput) (json.RawMessage, error) {
	request := map[string]any{
		"query":     `mutation CreateInternalTransfer($orgId: ID!, $input: CreateInternalTransferInput!) { createInternalTransfer(orgId: $orgId, input: $input) { id } }`,
		"variables": map[string]any{"orgId": orgID, "input": input},
	}
	data, err := c.do(ctx, http.MethodPost, "/graphql", request)
	if err != nil {
		return nil, err
	}
	var response struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode GraphQL response: %w", err)
	}
	if len(response.Errors) > 0 {
		messages := make([]string, 0, len(response.Errors))
		for _, item := range response.Errors {
			messages = append(messages, item.Message)
		}
		return nil, fmt.Errorf("create internal transfer: %s", strings.Join(messages, "; "))
	}
	return json.RawMessage(data), nil
}

func (c *Client) BulkUpdateTransactionState(ctx context.Context, orgID string, input BulkStateRequest) (*BulkStateResponse, error) {
	if len(input.TransactionIDs) == 0 {
		return nil, fmt.Errorf("at least one transaction id is required")
	}
	var response BulkStateResponse
	path := "/v3/orgs/" + url.PathEscape(orgID) + "/transactions/bulk-state"
	if err := c.doJSON(ctx, http.MethodPost, path, input, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) Transaction(ctx context.Context, orgID, transactionID string) (json.RawMessage, error) {
	path := "/v3/orgs/" + url.PathEscape(orgID) + "/transactions/" + url.PathEscape(transactionID)
	data, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("transaction response was not valid JSON")
	}
	return json.RawMessage(data), nil
}

// TransactionReviewDetail returns the state representation used by the
// product's Needs Review panel. Unlike the compact v3 read, it includes the
// current transaction, pending lines, pending exchange rates, and existing
// categorization needed to make a review decision.
func (c *Client) TransactionReviewDetail(ctx context.Context, orgID, transactionID string) (json.RawMessage, error) {
	path := "/orgs/" + url.PathEscape(orgID) + "/transactions/" + url.PathEscape(transactionID)
	data, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("transaction review response was not valid JSON")
	}
	return json.RawMessage(data), nil
}

// ResolveTransactionReviews applies or dismisses pending transaction changes.
// Dismissing pending changes is intentionally separate from ignoring the
// transaction itself.
func (c *Client) ResolveTransactionReviews(ctx context.Context, orgID, action string, transactionIDs []string) (*BulkStateResponse, error) {
	if action != TransactionReviewAccept && action != TransactionReviewIgnore {
		return nil, fmt.Errorf("unsupported transaction review action %q", action)
	}
	if len(transactionIDs) == 0 {
		return nil, fmt.Errorf("at least one transaction id is required")
	}
	items := make([]TransactionReviewItem, 0, len(transactionIDs))
	for _, transactionID := range transactionIDs {
		items = append(items, TransactionReviewItem{TransactionID: transactionID})
	}
	var response BulkStateResponse
	query := url.Values{"action": {action}}
	path := "/v3/orgs/" + url.PathEscape(orgID) + "/transactions/resolve?" + query.Encode()
	data, err := c.do(ctx, http.MethodPut, path, TransactionReviewRequest{Transactions: items})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(data)) == "" {
		response.Success = true
		response.Processed = len(items)
		response.SuccessCount = len(items)
		return &response, nil
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode transaction review response: %w", err)
	}
	return &response, nil
}

// TransactionWithAccountingDetails uses the legacy read representation because
// the v3 transaction endpoint intentionally omits saved accounting details.
func (c *Client) TransactionWithAccountingDetails(ctx context.Context, orgID, transactionID string) (json.RawMessage, error) {
	path := "/txns/" + url.PathEscape(orgID) + "/" + url.PathEscape(transactionID)
	data, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("transaction accounting-details response was not valid JSON")
	}
	return json.RawMessage(data), nil
}

func (c *Client) BulkTransactionStateStatus(ctx context.Context, orgID, workflowID string) (*BulkStateResponse, error) {
	var response BulkStateResponse
	path := "/v3/orgs/" + url.PathEscape(orgID) + "/transactions/bulk-state/" + url.PathEscape(workflowID)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// CategorizeTransaction sends the complete Bitwave categorization DTO. The
// shape is deliberately json.RawMessage because it is a tagged union whose
// required fields depend on the transaction and categorization type.
func (c *Client) CategorizeTransaction(ctx context.Context, orgID, transactionID string, body json.RawMessage) error {
	path := "/v3/orgs/" + url.PathEscape(orgID) + "/transactions/" + url.PathEscape(transactionID)
	_, err := c.do(ctx, http.MethodPatch, path, body)
	return err
}

func (c *Client) BulkCategorizeTransactions(ctx context.Context, orgID string, body json.RawMessage) ([]BulkCategorizeResult, error) {
	path := "/v3/orgs/" + url.PathEscape(orgID) + "/transactions"
	data, err := c.do(ctx, http.MethodPut, path, body)
	if err != nil {
		return nil, err
	}
	var response []BulkCategorizeResult
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode bulk categorization response: %w", err)
	}
	return response, nil
}

func (c *Client) Categories(ctx context.Context, orgID string) ([]Category, error) {
	var result []Category
	var token string
	for {
		query := url.Values{"pageLimit": {"500"}}
		if token != "" {
			query.Set("paginationToken", token)
		}
		var response struct {
			Items    []Category `json:"items"`
			NextPage string     `json:"nextPage"`
		}
		path := "/org/" + url.PathEscape(orgID) + "/categories?" + query.Encode()
		if err := c.doJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
			return nil, err
		}
		result = append(result, response.Items...)
		if response.NextPage == "" || response.NextPage == token {
			return result, nil
		}
		token = response.NextPage
	}
}

func (c *Client) Contacts(ctx context.Context, orgID string) ([]Contact, error) {
	var result []Contact
	var token string
	for {
		query := url.Values{"pageLimit": {"500"}}
		if token != "" {
			query.Set("paginationToken", token)
		}
		var response struct {
			Items    []Contact `json:"items"`
			NextPage string    `json:"nextPage"`
		}
		path := "/contacts/" + url.PathEscape(orgID) + "?" + query.Encode()
		if err := c.doJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
			return nil, err
		}
		result = append(result, response.Items...)
		if response.NextPage == "" || response.NextPage == token {
			return result, nil
		}
		token = response.NextPage
	}
}

func (c *Client) AccountingConnections(ctx context.Context, orgID string) ([]AccountingConnection, error) {
	var response struct {
		Connections []AccountingConnection `json:"connections"`
	}
	path := "/orgs/" + url.PathEscape(orgID) + "/accounting-connections"
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	return response.Connections, nil
}

func (c *Client) CreateManualAccountingConnection(ctx context.Context, orgID string) (*CreateManualAccountingConnectionResponse, error) {
	var response CreateManualAccountingConnectionResponse
	path := "/orgs/" + url.PathEscape(orgID) + "/connections/manual"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) CreateChartAccount(ctx context.Context, orgID string, input CreateChartAccountInput) (*CreateChartAccountResponse, error) {
	var response CreateChartAccountResponse
	path := "/org/" + url.PathEscape(orgID) + "/categories"
	if err := c.doJSON(ctx, http.MethodPost, path, input, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) CreateContact(ctx context.Context, orgID string, input CreateContactInput) (string, error) {
	request := map[string]any{
		"operationName": "CreateContact",
		"query":         `mutation CreateContact($orgId: ID!, $contact: CreateContactInput!) { createContact(orgId: $orgId, contact: $contact) }`,
		"variables":     map[string]any{"orgId": orgID, "contact": input},
	}
	data, err := c.doEndpoint(ctx, http.MethodPost, c.RulesMutationURL, request, true)
	if err != nil {
		return "", err
	}
	var response struct {
		Data struct {
			CreateContact string `json:"createContact"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return "", fmt.Errorf("decode create contact response: %w", err)
	}
	if err := graphqlErrors(response.Errors); err != nil {
		return "", err
	}
	if strings.TrimSpace(response.Data.CreateContact) == "" {
		return "", fmt.Errorf("create contact returned an empty id")
	}
	return response.Data.CreateContact, nil
}

func (c *Client) UpdateContact(ctx context.Context, orgID string, input json.RawMessage) (json.RawMessage, error) {
	request := map[string]any{
		"operationName": "UpdateContact",
		"query":         `mutation UpdateContact($orgId: ID!, $contact: UpdateContactInput!) { updateContact(orgId: $orgId, contact: $contact) { id name enabled defaultExpenseCategoryId defaultRevenueCategoryId accountingConnectionId } }`,
		"variables":     map[string]any{"orgId": orgID, "contact": input},
	}
	data, err := c.doEndpoint(ctx, http.MethodPost, c.RulesMutationURL, request, true)
	if err != nil {
		return nil, err
	}
	var response struct {
		Data struct {
			UpdateContact json.RawMessage `json:"updateContact"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode update contact response: %w", err)
	}
	if err := graphqlErrors(response.Errors); err != nil {
		return nil, err
	}
	if len(response.Data.UpdateContact) == 0 || string(response.Data.UpdateContact) == "null" {
		return nil, fmt.Errorf("update contact returned no contact")
	}
	return response.Data.UpdateContact, nil
}

func (c *Client) DisableContacts(ctx context.Context, orgID string) error {
	return c.runSuccessMutation(ctx, "DisableContacts", `mutation DisableContacts($orgId: ID!) { disableContacts(orgId: $orgId) { success } }`, orgID, "disableContacts")
}

func (c *Client) UpdateCategoryEnabled(ctx context.Context, orgID, categoryID string, enabled bool) (json.RawMessage, error) {
	request := map[string]any{
		"operationName": "UpdateCategory",
		"query":         `mutation UpdateCategory($orgId: ID!, $id: ID!, $enabled: Boolean!) { updateCategory(orgId: $orgId, id: $id, enabled: $enabled) { id name enabled } }`,
		"variables":     map[string]any{"orgId": orgID, "id": categoryID, "enabled": enabled},
	}
	data, err := c.doEndpoint(ctx, http.MethodPost, c.RulesMutationURL, request, true)
	if err != nil {
		return nil, err
	}
	var response struct {
		Data struct {
			UpdateCategory json.RawMessage `json:"updateCategory"`
		} `json:"data"`
		Errors []graphQLError `json:"errors"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode update category response: %w", err)
	}
	if err := graphqlErrors(response.Errors); err != nil {
		return nil, err
	}
	if len(response.Data.UpdateCategory) == 0 || string(response.Data.UpdateCategory) == "null" {
		return nil, fmt.Errorf("update category returned no category")
	}
	return response.Data.UpdateCategory, nil
}

func (c *Client) DisableCategories(ctx context.Context, orgID string) error {
	return c.runSuccessMutation(ctx, "DisableCategories", `mutation DisableCategories($orgId: ID!) { disableCategories(orgId: $orgId) { success } }`, orgID, "disableCategories")
}

func (c *Client) runSuccessMutation(ctx context.Context, operation, query, orgID, field string) error {
	request := map[string]any{"operationName": operation, "query": query, "variables": map[string]any{"orgId": orgID}}
	data, err := c.doEndpoint(ctx, http.MethodPost, c.RulesMutationURL, request, true)
	if err != nil {
		return err
	}
	var response struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []graphQLError             `json:"errors"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return fmt.Errorf("decode %s response: %w", operation, err)
	}
	if err := graphqlErrors(response.Errors); err != nil {
		return err
	}
	var result struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(response.Data[field], &result); err != nil {
		return fmt.Errorf("decode %s result: %w", operation, err)
	}
	if !result.Success {
		return fmt.Errorf("%s was rejected", operation)
	}
	return nil
}
