package operations

import (
	"fmt"

	"github.com/bitwave-io/bitwave-cli/internal/operation"

	"github.com/bitwave-io/bitwave-cli/internal/bitwave/config"
	"github.com/bitwave-io/bitwave-cli/internal/bitwave/shares"
)

func newShareCmd() *operation.Definition {
	var (
		to        string
		journalId string
		message   string
		ttlHours  int
		dryRun    bool
	)
	cmd := &operation.Definition{
		Use:   "share",
		Short: "Send a time-limited read-only link to a journal via email",
		Long: `Generate a tokenized URL recipients can use to view the named journal
read-only, and email the link to --to.

Auth:
  Sharing requires a CLOUD workspace: the server tokenizes the live journal
  on behalf of your org, so ` + "`bitwave auth login`" + ` + ` + "`bitwave org use`" + ` must
  already be set up and the workspace must have been created with
  ` + "`bitwave init --cloud`" + `. A local workspace cannot be shared — every shared
  workspace belongs to an org, which is what bounds who may read it.

What gets sent:
  No payload; the server reads from the existing cloud workspace and emits
  a tokenized read-only link.

Examples:
  bitwave share --to teammate@example.com
  bitwave share --to teammate@example.com --message "May expenses for review"
  bitwave share --to teammate@example.com --ttl 24       # link expires in 24h
  bitwave share --to teammate@example.com --dry-run      # don't actually send`,
		RunE: func(cmd *operation.Call, _ []string) error {
			if to == "" {
				return fmt.Errorf("--to is required")
			}
			cfg, _, err := loadCwdConfig(cmd.Context())
			if err != nil {
				return err
			}
			s, _, _, err := resolveStore(cmd.Context())
			if err != nil {
				return err
			}
			jId, err := resolveJournal(cmd.Context(), s, cfg, journalId)
			if err != nil {
				return err
			}

			if dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "DRY RUN: would POST share for journal %s to %s (ttl=%dh)\n",
					jId, to, ttlHours)
				return nil
			}

			if cfg.Mode == config.ModeLocal {
				// There is no anonymous share any more: a local workspace has
				// no org, and only an org-owned cloud workspace can be shared.
				return fmt.Errorf("sharing requires a cloud workspace: run `bitwave init --cloud` (after `bitwave auth login` + `bitwave org use`) and share from there")
			}
			if cfg.OrgId == "" {
				return fmt.Errorf(".bitwave.toml is missing org_id for cloud share")
			}
			if cfg.WorkspaceId == "" {
				return fmt.Errorf(".bitwave.toml is missing workspace_id for cloud share")
			}

			req := shares.CreateRequest{
				RecipientEmail: to,
				Message:        message,
				TTLHours:       ttlHours,
			}

			c := newShareClient(cmd.Context(), cfg.OrgId, cfg.WorkspaceId)
			resp, err := c.Create(cmd.Context(), jId, req)
			if err != nil {
				return fmt.Errorf("create share: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Shared journal %s with %s\n", jId, to)
			fmt.Fprintf(cmd.OutOrStdout(), "  URL:     %s\n", resp.URL)
			fmt.Fprintf(cmd.OutOrStdout(), "  Expires: %s\n", resp.ExpiresAt.Local().Format("2006-01-02 15:04 MST"))
			fmt.Fprintf(cmd.OutOrStdout(), "  ShareId: %s\n", resp.ShareId)
			if resp.EmailFailure != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "  Warning: email send failed (%s); the link is still active.\n", resp.EmailFailure)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "Recipient email address (required)")
	cmd.Flags().StringVar(&journalId, "journal", "", "Journal id (defaults to .bitwave.toml default_journal)")
	cmd.Flags().StringVar(&message, "message", "", "Optional message to include in the email")
	cmd.Flags().IntVar(&ttlHours, "ttl", 168, "Lifetime of the share link in hours (default 7 days)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print what would be sent without contacting the cloud ledger")
	return cmd
}

func newSharesCmd() *operation.Definition {
	cmd := &operation.Definition{
		Use:   "shares",
		Short: "List or revoke journal shares",
	}
	cmd.AddCommand(newSharesListCmd())
	cmd.AddCommand(newSharesRevokeCmd())
	return cmd
}

func newSharesListCmd() *operation.Definition {
	var journalId string
	cmd := &operation.Definition{
		Use:   "list",
		Short: "List shares for a journal",
		RunE: func(cmd *operation.Call, _ []string) error {
			cfg, _, err := loadCwdConfig(cmd.Context())
			if err != nil {
				return err
			}
			if cfg.OrgId == "" {
				return fmt.Errorf(".bitwave.toml is missing org_id")
			}
			s, _, _, err := resolveStore(cmd.Context())
			if err != nil {
				return err
			}
			jId, err := resolveJournal(cmd.Context(), s, cfg, journalId)
			if err != nil {
				return err
			}
			c := newShareClient(cmd.Context(), cfg.OrgId, cfg.WorkspaceId)
			list, err := c.List(cmd.Context(), jId)
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "(no shares)")
				return nil
			}
			for _, s := range list {
				fmt.Fprintf(cmd.OutOrStdout(), "%-36s  %-9s  %-32s  expires %s\n",
					s.ShareId, s.Status, s.RecipientEmail, s.ExpiresAt.Local().Format("2006-01-02 15:04"))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&journalId, "journal", "", "Journal id (defaults to .bitwave.toml default_journal)")
	return cmd
}

func newSharesRevokeCmd() *operation.Definition {
	var journalId string
	cmd := &operation.Definition{
		Use:   "revoke <shareId>",
		Short: "Revoke an active share link",
		Args:  operation.ExactArgs(1),
		RunE: func(cmd *operation.Call, args []string) error {
			cfg, _, err := loadCwdConfig(cmd.Context())
			if err != nil {
				return err
			}
			if cfg.OrgId == "" {
				return fmt.Errorf(".bitwave.toml is missing org_id")
			}
			s, _, _, err := resolveStore(cmd.Context())
			if err != nil {
				return err
			}
			jId, err := resolveJournal(cmd.Context(), s, cfg, journalId)
			if err != nil {
				return err
			}
			c := newShareClient(cmd.Context(), cfg.OrgId, cfg.WorkspaceId)
			out, err := c.Revoke(cmd.Context(), jId, args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Revoked share %s (status=%s)\n", out.ShareId, out.Status)
			return nil
		},
	}
	cmd.Flags().StringVar(&journalId, "journal", "", "Journal id (defaults to .bitwave.toml default_journal)")
	return cmd
}
