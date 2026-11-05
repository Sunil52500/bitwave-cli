package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	op "github.com/bitwave-io/bitwave-cli/internal/operation"

	"github.com/bitwave-io/bitwave-cli/internal/apierr"
	"github.com/bitwave-io/bitwave-cli/internal/orgreports"
)

// The inventory error triage runs on the Bitwave platform as a report run:
// a submit answers 202 with the run to poll, and the result is read back once
// the run succeeded. One command does the three steps so a hosted caller
// gets the explained groups in one call; a submit for a triage already under
// way, or already done, for the same view, inventory run and day attaches to
// it, so a call that timed out can simply be made again.

const inventoryErrorTriageOperation = "view-error-triage"

// inventoryErrorTriageHelp is the long help of the error-triage command;
// hosted callers see it as the tool description, so it names what comes back.
const inventoryErrorTriageHelp = `Read-only. Explains the errored actions of the active inventory run
(disposals the engine found no lots for): groups them by asset, inventory,
and wallet, and explains each group from the facts at its first shortfall:
the inventory balance, Bitwave's wallet balance, the third party's balance
when the organization has one, the transaction (trade legs without wallet
ids, manual, ignored, deleted, unpriced), and the view's mapping and start
date.

Each group names one cause (unpriced, trade_legs_missing_wallet,
wallet_not_in_inventory, lot_selection_failed, before_inventory_start,
manual_transaction_unverified, acquisition_not_in_inventory,
bitwave_missing_inflows, chain_short_or_transaction_suspect, unknown), a
confidence (fact, likely, unknown), the facts used, and a fix only when the
cause is a fact; every other group carries a handoff sentence saying what to
check. Nothing in the organization is changed.

The triage runs on the platform as a report run. The command submits it,
waits for it to finish (--timeout, default 10m), and returns the result; a
call for the same view, inventory run, and day attaches to the run already
started unless --refresh is given, so a call that ran out of time can be
made again. --submit-only returns the report run without waiting, and
--report-run REPORT_RUN_ID reads or waits for a run submitted earlier.

--as-of YYYY-MM-DD sets the report end date in the organization's timezone
(default: today). The result carries truncated=true when more than twenty
pages of errored actions exist, and staleRun=true when the view has run
again since the triage was submitted.`

// inventoryErrorTriageSubmit is the platform's answer to a submit.
type inventoryErrorTriageSubmit struct {
	ReportRunID string `json:"reportRunId"`
	RunID       string `json:"runId"`
	AsOfSEC     int64  `json:"asOfSEC"`
	Status      string `json:"status"`
	Attached    bool   `json:"attached"`
}

// inventoryErrorTriageRun is the platform's answer to a read of one run.
type inventoryErrorTriageRun struct {
	ReportRunID string          `json:"reportRunId"`
	Status      string          `json:"status"`
	Result      json.RawMessage `json:"result,omitempty"`
}

type inventoryErrorTriageFlags struct {
	orgID        string
	asOf         string
	refresh      bool
	submitOnly   bool
	reportRunID  string
	timeout      time.Duration
	pollInterval time.Duration
	dryRun       bool
	out          string
}

func newInventoryErrorTriageCmd(operation adminOperation) *op.Definition {
	var f inventoryErrorTriageFlags
	cmd := &op.Definition{
		Use:   operation.Use,
		Short: operation.Short,
		Long:  operation.Long,
		Args:  op.ExactArgs(1),
		RunE: func(cmd *op.Call, args []string) error {
			return runInventoryErrorTriage(cmd, operation, args[0], f)
		},
	}
	cmd.Flags().StringVar(&f.orgID, "org", "", "Organization ID override")
	cmd.Flags().StringVar(&f.asOf, "as-of", "", "Report end date as YYYY-MM-DD in the organization's timezone (default: today)")
	cmd.Flags().BoolVar(&f.refresh, "refresh", false, "Start a new triage even when one for the same view, run, and day exists")
	cmd.Flags().BoolVar(&f.submitOnly, "submit-only", false, "Submit the triage and return the report run without waiting for the result")
	cmd.Flags().StringVar(&f.reportRunID, "report-run", "", "Read or wait for a report run submitted earlier instead of submitting")
	cmd.Flags().DurationVar(&f.timeout, "timeout", 10*time.Minute, "Maximum time to wait for the result")
	cmd.Flags().DurationVar(&f.pollInterval, "poll-interval", 2*time.Second, "Delay between status checks")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "Print the exact submit request without contacting Bitwave")
	cmd.Flags().StringVarP(&f.out, "out", "o", "", "Save the JSON result to a file")
	return cmd
}

func runInventoryErrorTriage(cmd *op.Call, operation adminOperation, viewID string, f inventoryErrorTriageFlags) error {
	if err := validateImportTiming(f.timeout, f.pollInterval); err != nil {
		return err
	}
	asOf := strings.TrimSpace(f.asOf)
	if asOf != "" {
		if _, err := time.Parse("2006-01-02", asOf); err != nil {
			return errors.New("--as-of must be a date as YYYY-MM-DD")
		}
	}
	orgID, err := resolveReportOrg(cmd.Context(), f.orgID)
	if err != nil {
		return err
	}
	client := newReportsClient(cmd.Context(), orgID)
	base := "/orgs/" + url.PathEscape(orgID) + "/inventory-views/" + url.PathEscape(viewID) + "/error-triage"

	reportRunID := strings.TrimSpace(f.reportRunID)
	var submitted *inventoryErrorTriageSubmit
	if reportRunID == "" {
		var query []string
		if asOf != "" {
			query = append(query, "asOf="+asOf)
		}
		if f.refresh {
			query = append(query, "refresh=true")
		}
		submitPath, err := appendAPIQuery(base, query)
		if err != nil {
			return err
		}
		if f.dryRun {
			endpoint, err := client.RawEndpoint(orgreports.APIServiceCore, submitPath)
			if err != nil {
				return err
			}
			preview := apiPreview(http.MethodPost, endpoint, orgreports.APIServiceCore, nil)
			preview["adminArea"] = operation.Area
			preview["adminOperation"] = operation.Name
			preview["then"] = "poll GET " + base + "/{reportRunId} until the run succeeded, failed, or timed out"
			notSubmitted := false
			return writeJSON(cmd.OutOrStdout(), mutationEnvelope{SchemaVersion: "1", Status: "preview", Operation: operation.Name, Organization: orgID, DryRun: true, Submitted: &notSubmitted, Note: dryRunNote, Request: preview})
		}
		data, err := client.RawRequest(cmd.Context(), orgreports.APIServiceCore, http.MethodPost, submitPath, nil)
		if err != nil {
			return fmt.Errorf("%s: submit: %w", operation.Name, err)
		}
		submitted = &inventoryErrorTriageSubmit{}
		if err := json.Unmarshal(data, submitted); err != nil || submitted.ReportRunID == "" {
			return fmt.Errorf("%s: submit answered without a report run: %s", operation.Name, strings.TrimSpace(string(data)))
		}
		reportRunID = submitted.ReportRunID
		if f.submitOnly {
			return writeJSON(cmd.OutOrStdout(), mutationEnvelope{SchemaVersion: "1", Status: "success", Operation: operation.Name, Organization: orgID, Result: submitted})
		}
	} else if f.dryRun {
		return errors.New("--dry-run previews a submit; it cannot be combined with --report-run")
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), f.timeout)
	defer cancel()
	run, err := waitForInventoryErrorTriage(ctx, client, base, reportRunID, f.pollInterval)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return inventoryErrorTriagePending(cmd, operation.Name, orgID, viewID, reportRunID, submitted, f.timeout)
		}
		return fmt.Errorf("%s: wait for report run %s: %w", operation.Name, reportRunID, err)
	}
	if run.Status != "succeeded" {
		_ = writeJSON(cmd.OutOrStdout(), mutationEnvelope{
			SchemaVersion: "1", Status: "error", Operation: operation.Name, Organization: orgID,
			Result: map[string]any{"reportRunId": reportRunID, "status": run.Status, "submitted": submitted},
			Error: &reportError{
				Code: "triage_" + strings.ReplaceAll(run.Status, "-", "_"), Message: "the triage report run " + reportRunID + " " + run.Status,
				Retryable: true, Suggestion: "Run the command again with --refresh to start a new triage; a run that fails twice is a platform problem to report with the report run id.",
			},
		})
		return errors.New("triage report run " + reportRunID + " " + run.Status)
	}
	result := map[string]any{"reportRunId": reportRunID, "status": run.Status, "result": run.Result}
	if submitted != nil {
		result["runId"] = submitted.RunID
		result["asOfSEC"] = submitted.AsOfSEC
		result["attached"] = submitted.Attached
	}
	envelope := mutationEnvelope{SchemaVersion: "1", Status: "success", Operation: operation.Name, Organization: orgID, Result: result}
	if f.out != "" && f.out != "-" {
		data, err := json.MarshalIndent(envelope, "", "  ")
		if err != nil {
			return err
		}
		return writeAPIResponse(cmd, f.out, data)
	}
	return writeJSON(cmd.OutOrStdout(), envelope)
}

// inventoryErrorTriagePending answers a wait that ran out of time: the run is
// still going, and the same command attaches to it when made again.
func inventoryErrorTriagePending(cmd *op.Call, operationName, orgID, viewID, reportRunID string, submitted *inventoryErrorTriageSubmit, timeout time.Duration) error {
	_ = writeJSON(cmd.OutOrStdout(), mutationEnvelope{
		SchemaVersion: "1", Status: "error", Operation: operationName, Organization: orgID,
		Result: map[string]any{"reportRunId": reportRunID, "status": "running", "submitted": submitted},
		Error: &reportError{
			Code: "triage_pending", Message: fmt.Sprintf("the triage report run %s was still running after %s", reportRunID, timeout),
			Retryable: true, Suggestion: fmt.Sprintf("Run the command again for view %s: it attaches to the same report run (or pass --report-run %s) and returns the result once the run finished.", viewID, reportRunID),
		},
	})
	return errors.New("triage report run " + reportRunID + " still running after " + timeout.String())
}

// waitForInventoryErrorTriage polls the run until it is no longer new or
// running. A platform hiccup (429, 5xx, a network error) is retried with a
// growing delay, up to four times in a row; anything else is final.
func waitForInventoryErrorTriage(ctx context.Context, client *orgreports.Client, base, reportRunID string, interval time.Duration) (*inventoryErrorTriageRun, error) {
	path := base + "/" + url.PathEscape(reportRunID)
	consecutiveFailures := 0
	for {
		data, err := client.RawRequest(ctx, orgreports.APIServiceCore, http.MethodGet, path, nil)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !retryableTriagePollError(err) || consecutiveFailures >= 4 {
				return nil, err
			}
			consecutiveFailures++
			if err := waitImportPoll(ctx, interval*time.Duration(consecutiveFailures)); err != nil {
				return nil, err
			}
			continue
		}
		consecutiveFailures = 0
		var run inventoryErrorTriageRun
		if err := json.Unmarshal(data, &run); err != nil {
			return nil, fmt.Errorf("read report run %s: %w", reportRunID, err)
		}
		if run.Status != "new" && run.Status != "running" {
			return &run, nil
		}
		if err := waitImportPoll(ctx, interval); err != nil {
			return nil, err
		}
	}
}

func retryableTriagePollError(err error) bool {
	var apiError *apierr.Error
	if errors.As(err, &apiError) {
		return apiError.Status == http.StatusTooManyRequests || apiError.Status >= 500
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}
