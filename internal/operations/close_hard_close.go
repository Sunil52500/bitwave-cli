package operations

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	op "github.com/bitwave-io/bitwave-cli/internal/operation"

	"github.com/bitwave-io/bitwave-cli/internal/orgreports"
)

// The hard-close date is the organization's accounting period lock. api-svc
// keeps it on the org and enforces it on every transaction command; the Close
// Dashboard shows it and completing a checklist run advances it. These
// commands read and set it directly.

const hardCloseLockEffect = `Transactions dated on or before the date, in the organization's timezone
(up to its accounting-day cutoff when one is set), are locked: they can still
be synced, priced and ignored, but their changes cannot be accepted and they
cannot be categorized, edited or combined until the period is reopened.`

func newCloseHardCmd() *op.Definition {
	var f closeMutationFlags
	var endDate string
	cmd := &op.Definition{
		Use:   "hard-close",
		Short: "Set the organization's hard-close date (the accounting period lock)",
		Long: `Set the organization's hard-close date: the accounting period lock.

` + hardCloseLockEffect + `

Completing a close checklist run (` + "`bitwave close complete`" + `) already moves the
date forward to the run's end date. Set it here to lock or reopen periods
outside a checklist close. Read the current date first with
` + "`bitwave close hard-close get`" + `.

Bitwave refuses a date while any uncategorized, unignored transaction falls on
or before it. Moving the date earlier reopens closed periods and needs
organization update permission as well as period close update. --end-date is
YYYY-MM-DD. Requires --yes and supports --dry-run.`,
		Args: op.NoArgs,
		RunE: func(cmd *op.Call, _ []string) error {
			if strings.TrimSpace(endDate) == "" {
				return errors.New("--end-date is required")
			}
			query := `mutation updateOrg($orgId: ID!, $hardCloseDate: String!) { updateOrg(orgId: $orgId, hardCloseDate: $hardCloseDate) { id } }`
			orgID, err := resolveReportOrg(cmd.Context(), f.orgID)
			if err != nil {
				return err
			}
			payload := map[string]any{"query": query, "variables": map[string]any{"orgId": orgID, "hardCloseDate": endDate}}
			return closeMutationResolved(cmd, f, orgID, "set-hard-close-date", orgreports.APIServiceApp, http.MethodPost, "/graphql", payload, nil)
		},
	}
	addCloseMutationFlags(cmd, &f)
	cmd.Flags().StringVar(&endDate, "end-date", "", "Hard-close date (YYYY-MM-DD)")
	cmd.AddCommand(newCloseHardGetCmd())
	return cmd
}

// hardCloseStatus is the read's output: the lock as stored, and one sentence
// naming the last locked moment so a caller need not work it out.
type hardCloseStatus struct {
	SchemaVersion string  `json:"schemaVersion"`
	Organization  string  `json:"organization"`
	Locked        bool    `json:"locked"`
	HardCloseDate *string `json:"hardCloseDate"`
	Timezone      string  `json:"timezone,omitempty"`
	CutoffTime    string  `json:"cutoffTime,omitempty"`
	LockedThrough string  `json:"lockedThrough"`
}

const gqlHardCloseGet = `query HardCloseGet($orgId: ID!, $allowCache: Boolean) { org(id: $orgId, allowCache: $allowCache) { id timezone cutoffTime hardCloseDate } }`

func newCloseHardGetCmd() *op.Definition {
	var orgID string
	cmd := &op.Definition{
		Use:   "get",
		Short: "Read the organization's hard-close date (the accounting period lock)",
		Long: `Read the organization's hard-close date: the accounting period lock. Answers
whether accounting periods are locked or closed, and through which day.

` + hardCloseLockEffect + `

Returns locked, hardCloseDate (YYYY-MM-DD, or null when no period is locked),
timezone, cutoffTime and lockedThrough, a sentence naming the last locked
moment. In the app the date shows on the Close dashboard as "Transactions
locked up to". Completing a close checklist run moves it forward to the run's
end date; ` + "`bitwave close hard-close`" + ` sets it.`,
		Args: op.NoArgs,
		RunE: func(cmd *op.Call, _ []string) error {
			resolved, err := resolveReportOrg(cmd.Context(), orgID)
			if err != nil {
				return err
			}
			// Uncached: a lock moved moments ago must read as moved.
			body, err := json.Marshal(map[string]any{
				"query":     gqlHardCloseGet,
				"variables": map[string]any{"orgId": resolved, "allowCache": false},
			})
			if err != nil {
				return err
			}
			client := newReportsClient(cmd.Context(), resolved)
			data, err := client.RawRequestBytes(cmd.Context(), orgreports.APIServiceApp, http.MethodPost, "/graphql", body, nil)
			if err != nil {
				return err
			}
			if err := graphqlResponseError(data); err != nil {
				return err
			}
			var response struct {
				Data struct {
					Org *struct {
						Timezone      string  `json:"timezone"`
						CutoffTime    *string `json:"cutoffTime"`
						HardCloseDate *string `json:"hardCloseDate"`
					} `json:"org"`
				} `json:"data"`
			}
			if err := json.Unmarshal(data, &response); err != nil {
				return fmt.Errorf("decode organization: %w", err)
			}
			org := response.Data.Org
			if org == nil {
				return fmt.Errorf("organization %s was not returned", resolved)
			}
			status := hardCloseStatus{SchemaVersion: "1", Organization: resolved, Timezone: org.Timezone}
			if org.CutoffTime != nil {
				status.CutoffTime = strings.TrimSpace(*org.CutoffTime)
			}
			if org.HardCloseDate != nil && strings.TrimSpace(*org.HardCloseDate) != "" {
				day := strings.TrimSpace(*org.HardCloseDate)
				if len(day) > 10 {
					day = day[:10]
				}
				status.Locked = true
				status.HardCloseDate = &day
			}
			status.LockedThrough = describeHardCloseLock(status)
			return writeJSON(cmd.OutOrStdout(), status)
		},
	}
	addCloseReadFlags(cmd, &orgID)
	return cmd
}

func describeHardCloseLock(s hardCloseStatus) string {
	if !s.Locked {
		return "No period is locked: no hard-close date is set."
	}
	zone := s.Timezone
	if zone == "" {
		zone = "the organization's timezone"
	}
	if s.CutoffTime != "" {
		return fmt.Sprintf("Transactions before %s on %s (%s), the accounting-day cutoff, are locked.", s.CutoffTime, *s.HardCloseDate, zone)
	}
	return fmt.Sprintf("Transactions through the end of the day %s (%s) are locked.", *s.HardCloseDate, zone)
}
