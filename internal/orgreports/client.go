// Package orgreports is the HTTP client for Bitwave organization reports.
// It is intentionally separate from the CLI ledger workspace client: these
// reports run against the organization's product data, not ledger journals.
package orgreports

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bitwave-io/bitwave-cli/internal/apierr"
)

const BalanceReportType = "balance-report"

type Input struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type StartRequest struct {
	ReportType string  `json:"reportType"`
	Inputs     []Input `json:"inputs"`
}

type Run struct {
	SuccessfullyStarted bool   `json:"successfullyStarted"`
	ReportRunID         string `json:"reportRunId"`
	Error               string `json:"error,omitempty"`
}

type RunStatus struct {
	Status string `json:"status"`
}

type LegacyBalanceInput struct {
	EndDate       string
	GroupBy       string
	SubsidiaryIDs []string
}

type LegacyRun struct {
	ID string `json:"id"`
}

type LegacyReport struct {
	Data struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"data"`
	Links map[string]Link `json:"links"`
}

type Link struct {
	Href   string `json:"href"`
	Method string `json:"method"`
}

// ReportData is the deployed report-result representation. It provides the
// same columns and cell rows shown by the web application and is used as a
// compatibility download path when the dedicated CSV route is unavailable.
type ReportData struct {
	ReportType  string      `json:"reportType"`
	ReportRunID string      `json:"reportRunId"`
	Columns     []string    `json:"columns"`
	Rows        []ReportRow `json:"rows"`
}

type ReportRow struct {
	Cells []string    `json:"cells"`
	Rows  []ReportRow `json:"rows"`
}

type Client struct {
	BaseURL          string
	API2URL          string
	TransactionsURL  string
	RulesQueryURL    string
	RulesMutationURL string
	TokenResolver    func() (string, error)
	HTTPClient       *http.Client
}

func New(baseURL string, tokenResolver func() (string, error)) *Client {
	baseURL = strings.TrimRight(baseURL, "/")
	rulesQueryURL := baseURL + "/graphql-reports"
	rulesMutationURL := baseURL + "/graphql"
	api2URL := baseURL
	// The transactions service (transactions.bitwave.io) was retired on
	// 2026-10-19; the API gateway at the core base URL routes every path it
	// served (/orgs/{orgId}/lookups, /transactions/summary_v2, ...) to api-svc.
	transactionsURL := baseURL
	if parsed, err := url.Parse(baseURL); err == nil && parsed.Hostname() == "api.bitwave.io" {
		api2URL = "https://api2.bitwave.io"
		rulesQueryURL = "https://api4.bitwave.io/graphql-reports"
		rulesMutationURL = "https://api-app.bitwave.io/graphql"
	}
	return &Client{
		BaseURL:          baseURL,
		API2URL:          api2URL,
		TransactionsURL:  transactionsURL,
		RulesQueryURL:    rulesQueryURL,
		RulesMutationURL: rulesMutationURL,
		TokenResolver:    tokenResolver,
		HTTPClient:       &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *Client) StartBalance(ctx context.Context, orgID string, inputs []Input) (*Run, error) {
	var run Run
	if err := c.doJSON(ctx, http.MethodPost, reportRunsPath(orgID), StartRequest{
		ReportType: BalanceReportType,
		Inputs:     inputs,
	}, &run); err != nil {
		return nil, err
	}
	if !run.SuccessfullyStarted || run.ReportRunID == "" {
		if run.Error == "" {
			run.Error = "server did not return a report run id"
		}
		return nil, fmt.Errorf("start balance report: %s", run.Error)
	}
	return &run, nil
}

// StartLegacyBalance uses the production-stable report controller that backs
// the web application's default Balance Report path. The V3 report-run API is
// retained separately because some deployments expose start/status without
// yet exposing result/download.
func (c *Client) StartLegacyBalance(ctx context.Context, orgID string, input LegacyBalanceInput) (*LegacyRun, error) {
	query := url.Values{
		"type":    {"BalanceReport"},
		"orgId":   {orgID},
		"endDate": {input.EndDate},
		"groupBy": {input.GroupBy},
	}
	for _, id := range input.SubsidiaryIDs {
		query.Add("subsidiaryIds[]", id)
	}
	var run LegacyRun
	if err := c.doJSON(ctx, http.MethodGet, "/reports/view?"+query.Encode(), nil, &run); err != nil {
		return nil, err
	}
	if run.ID == "" {
		return nil, fmt.Errorf("legacy balance report response did not include an id")
	}
	return &run, nil
}

func (c *Client) LegacyReport(ctx context.Context, orgID, runID string, includeDownloadURL bool) (*LegacyReport, error) {
	path := "/v2/orgs/" + url.PathEscape(orgID) + "/reports/" + url.PathEscape(runID)
	if includeDownloadURL {
		path += "?includeDownloadUrls=true"
	}
	var report LegacyReport
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &report); err != nil {
		return nil, err
	}
	if report.Data.Status == "" {
		return nil, fmt.Errorf("legacy report response did not include status")
	}
	return &report, nil
}

func (c *Client) DownloadLink(ctx context.Context, href string) ([]byte, error) {
	resolved, err := url.Parse(href)
	if err != nil {
		return nil, fmt.Errorf("invalid report download URL: %w", err)
	}
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, err
	}
	if !resolved.IsAbs() {
		resolved = base.ResolveReference(resolved)
	}
	authenticated := strings.EqualFold(resolved.Scheme, base.Scheme) && strings.EqualFold(resolved.Host, base.Host)
	return c.doEndpoint(ctx, http.MethodGet, resolved.String(), nil, authenticated)
}

func (c *Client) Status(ctx context.Context, orgID, runID string) (*RunStatus, error) {
	var status RunStatus
	path := reportRunsPath(orgID) + "/" + url.PathEscape(runID) + "/status"
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &status); err != nil {
		return nil, err
	}
	if status.Status == "" {
		return nil, fmt.Errorf("report status response did not include status")
	}
	return &status, nil
}

func (c *Client) Download(ctx context.Context, orgID, runID string) ([]byte, error) {
	path := reportRunsPath(orgID) + "/" + url.PathEscape(runID) + "/download"
	return c.do(ctx, http.MethodGet, path, nil)
}

func (c *Client) Result(ctx context.Context, orgID, runID string) (*ReportData, error) {
	var result ReportData
	path := reportRunsPath(orgID) + "/" + url.PathEscape(runID)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	if len(result.Columns) == 0 {
		return nil, fmt.Errorf("report result did not include columns")
	}
	return &result, nil
}

func reportRunsPath(orgID string) string {
	return "/v2/orgs/" + url.PathEscape(orgID) + "/report-runs"
}

func (c *Client) doJSON(ctx context.Context, method, path string, body, dst any) error {
	data, err := c.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("decode %s %s response: %w", method, path, err)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	return c.doEndpoint(ctx, method, c.BaseURL+path, body, true)
}

func (c *Client) doEndpoint(ctx context.Context, method, endpoint string, body any, authenticated bool) ([]byte, error) {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	return c.doEndpointBytes(ctx, method, endpoint, data, authenticated, nil)
}

func (c *Client) doEndpointBytes(ctx context.Context, method, endpoint string, body []byte, authenticated bool, headers http.Header) ([]byte, error) {
	resp, err := c.doEndpointBytesDetailed(ctx, method, endpoint, body, authenticated, headers)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (c *Client) doEndpointBytesDetailed(ctx context.Context, method, endpoint string, body []byte, authenticated bool, headers http.Header) (*RawResponse, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	if authenticated {
		token, err := c.TokenResolver()
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json, text/csv")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, values := range headers {
		req.Header.Del(name)
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", method, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, apierr.Format(resp.StatusCode, method, endpoint, data)
	}
	return &RawResponse{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: data}, nil
}
