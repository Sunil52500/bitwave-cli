package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	op "github.com/bitwave-io/bitwave-cli/internal/operation"

	"github.com/bitwave-io/bitwave-cli/internal/orgreports"
)

// This is the union of Bitwave's current Add Source catalog and the legacy
// wallet adapter. Creation remains forward-compatible: an unlisted canonical
// network id is passed to the API, which is the final authority.
var organizationWalletNetworks = map[string]string{
	"aleo": "Aleo", "alot": "Dexalot", "apt": "Aptos", "arb": "Arbitrum",
	"aurora": "Aurora", "avaxc": "Avalanche C-Chain", "avaxp": "Avalanche P-Chain",
	"base": "Base", "bch": "Bitcoin Cash", "beacon": "Beacon", "bsc": "BNB Chain",
	"btc": "Bitcoin", "canton": "Canton", "casper": "Casper", "celo": "Celo",
	"cosmos": "Cosmos", "dash": "Dash", "doge": "Dogecoin", "dot": "Polkadot",
	"eos": "EOS", "eth": "Ethereum", "fil": "Filecoin", "flow": "Flow",
	"ftm": "Fantom", "gnosis": "Gnosis", "hbar": "Hedera", "imx": "Immutable X",
	"kava": "Kava", "kaia": "Kaia", "klay": "Klaytn", "ksm": "Kusama",
	"ltc": "Litecoin", "mina": "Mina", "near": "NEAR", "op": "Optimism",
	"osmo": "Osmosis", "polygon": "Polygon", "polyx": "Polymesh", "rose": "Oasis",
	"sol": "Solana", "stx": "Stacks", "terra": "Terra", "xlm": "Stellar",
	"xrp": "XRP", "zec": "Zcash", "zeta": "ZetaChain",
}

var organizationWalletNetworkAliases = map[string]string{
	"aptos": "apt", "arbitrum": "arb", "avalanche": "avaxc", "avalanche-c": "avaxc",
	"avalanche-p": "avaxp", "bitcoin": "btc", "bitcoin-cash": "bch", "bnb": "bsc",
	"bnb-chain": "bsc", "dogecoin": "doge", "ethereum": "eth", "fantom": "ftm",
	"filecoin": "fil", "hedera": "hbar", "immutable": "imx", "immutable-x": "imx",
	"kusama": "ksm", "litecoin": "ltc", "oasis": "rose", "optimism": "op",
	"osmosis": "osmo", "polkadot": "dot", "polymesh": "polyx", "solana": "sol",
	"stacks": "stx", "stellar": "xlm", "zcash": "zec", "zetachain": "zeta",
}

const organizationWalletSyncExpectation = "Wallet data typically appears within 15 minutes but can take up to 24 hours, depending on transaction history volume and network load."

// Wallet kinds accepted by `org wallets add`. blockchain is the modern
// accountBasedBlockchain contract; defi is a DeFi position wallet (wallet
// address + vault/pool/staking-contract address) synced by sync-coordinator.
const (
	orgWalletTypeBlockchain = "blockchain"
	orgWalletTypeDefi       = "defi"
)

type orgWalletInput struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Type is blockchain (default) or defi.
	Type      string `json:"type,omitempty"`
	Address   string `json:"address"`
	NetworkID string `json:"networkId"`
	// VaultAddress is required for defi wallets: the pool, vault, or staking
	// contract the position lives in (for Monad native staking this is the
	// fixed precompile 0x0000000000000000000000000000000000001000).
	VaultAddress string `json:"vaultAddress,omitempty"`
	// Protocol optionally labels the DeFi protocol; the backend resolves the
	// protocol from the vault address, so this is informational.
	Protocol                string         `json:"protocol,omitempty"`
	SubsidiaryID            string         `json:"subsidiaryId,omitempty"`
	AddressType             string         `json:"addressType,omitempty"`
	SyncStartDateSEC        int64          `json:"syncStartDateSEC,omitempty"`
	ViewKey                 string         `json:"viewKey,omitempty"`
	Metadata                map[string]any `json:"metadata,omitempty"`
	IsBalanceMonitoringOnly bool           `json:"isBalanceMonitoringOnly,omitempty"`
}

type orgWalletAddFlags struct {
	transactionMutationFlags
	input                 string
	name                  string
	walletType            string
	address               string
	vaultAddress          string
	protocol              string
	network               string
	subsidiary            string
	addressType           string
	syncStartDateSEC      int64
	viewKey               string
	balanceMonitoringOnly bool
	allowDuplicate        bool
	concurrency           int
}

func newOrgWalletsCmd() *op.Definition {
	cmd := &op.Definition{
		Use:     "wallets",
		Aliases: []string{"wallet"},
		Short:   "List or add wallets in the active Bitwave organization",
		Long: `Manage product wallets in the active Bitwave organization. This is
separate from the top-level local-ledger wallets command. Blockchain addresses
use the same accountBasedBlockchain creation contract as Bitwave Add Source.

After creation, data typically appears within 15 minutes but can take up to 24
hours depending on transaction history volume and network load.`,
	}
	cmd.AddCommand(newOrgWalletsListCmd(), newOrgWalletsNetworksCmd(), newOrgWalletsAddCmd(), newOrgWalletsRollupCmd(), newOrgWalletResyncCmd(), newOrgWalletDefiScheduleCmd())
	return cmd
}

func newOrgWalletsListCmd() *op.Definition {
	var orgID string
	var jsonOutput bool
	cmd := &op.Definition{
		Use:   "list",
		Short: "List organization wallets with network, address, and subsidiary",
		RunE: func(cmd *op.Call, _ []string) error {
			resolvedOrg, err := resolveReportOrg(cmd.Context(), orgID)
			if err != nil {
				return err
			}
			client := newReportsClient(cmd.Context(), resolvedOrg)
			wallets, err := client.Wallets(cmd.Context(), resolvedOrg)
			if err != nil {
				return fmt.Errorf("list organization wallets: %w", err)
			}
			if jsonOutput {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"schemaVersion": "1", "organization": resolvedOrg, "wallets": wallets})
			}
			if len(wallets) == 0 {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "(no organization wallets)")
				return err
			}
			for _, wallet := range wallets {
				address := wallet.Address
				if address == "" && len(wallet.Addresses) > 0 {
					address = strings.Join(wallet.Addresses, ",")
				}
				if wallet.VaultAddress != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\tvault=%s\n", wallet.ID, wallet.Name, wallet.NetworkID, address, wallet.SubsidiaryID, wallet.VaultAddress)
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", wallet.ID, wallet.Name, wallet.NetworkID, address, wallet.SubsidiaryID)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID override")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit machine-readable JSON")
	return cmd
}

func newOrgWalletsNetworksCmd() *op.Definition {
	var jsonOutput bool
	cmd := &op.Definition{
		Use:   "networks",
		Short: "List canonical Bitwave blockchain network IDs",
		RunE: func(cmd *op.Call, _ []string) error {
			ids := make([]string, 0, len(organizationWalletNetworks))
			for id := range organizationWalletNetworks {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			if jsonOutput {
				items := make([]map[string]string, 0, len(ids))
				for _, id := range ids {
					items = append(items, map[string]string{"id": id, "name": organizationWalletNetworks[id]})
				}
				return writeJSON(cmd.OutOrStdout(), map[string]any{"schemaVersion": "1", "networks": items, "forwardCompatible": true})
			}
			for _, id := range ids {
				fmt.Fprintf(cmd.OutOrStdout(), "%-10s %s\n", id, organizationWalletNetworks[id])
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit machine-readable JSON")
	return cmd
}

func newOrgWalletsAddCmd() *op.Definition {
	var f orgWalletAddFlags
	cmd := &op.Definition{
		Use:   "add",
		Short: "Add one wallet, or a JSON batch, to the active organization",
		Long: `Add one organization wallet using flags, or supply --input with a JSON
array (or {"wallets": [...]}) for a batch. Address type defaults to address;
use --address-type hd for a BTC or DASH xpub/derivation key.

Use --type defi with --vault-address to add a DeFi position wallet: --address
is your wallet on the network and --vault-address is the pool / vault /
staking contract the position lives in (batch JSON: "type": "defi" plus
"vaultAddress"). DeFi wallets are synced by sync-coordinator; after creating
one, run 'bitwave org wallets defi-schedule WALLET' to start the position sync.

Each wallet needs a network and an address. The name defaults to the network
name and a shortened address (for example "Ethereum 0xfc48...1ee3"). In an
organization that uses subsidiaries the wallet must belong to one: when the
organization has only one subsidiary it is used, otherwise pass --subsidiary
(list them with 'bitwave org admin subsidiaries list' and ask which one owns
the wallet). Pass --yes to create; --dry-run only prints the request and does
not check it with the server. An address already in the organization is
skipped, not added again.

Creating a wallet starts asynchronous ingestion. Data typically appears within
15 minutes but can take up to 24 hours depending on transaction history volume
and network load.`,
		RunE: func(cmd *op.Call, _ []string) error { return runOrgWalletsAdd(cmd, f) },
	}
	addMutationFlags(cmd, &f.transactionMutationFlags)
	cmd.Flags().StringVarP(&f.input, "input", "i", "", "Wallet JSON file, or - for stdin")
	cmd.Flags().StringVar(&f.name, "name", "", "Wallet name (single-wallet mode; defaults to network and shortened address)")
	cmd.Flags().StringVar(&f.walletType, "type", orgWalletTypeBlockchain, "Wallet kind: blockchain or defi")
	cmd.Flags().StringVar(&f.address, "address", "", "Wallet address or HD derivation key")
	cmd.Flags().StringVar(&f.vaultAddress, "vault-address", "", "DeFi pool / vault / staking contract address (required with --type defi)")
	cmd.Flags().StringVar(&f.protocol, "protocol", "", "Optional DeFi protocol label (--type defi)")
	cmd.Flags().StringVar(&f.network, "network", "", "Canonical network ID or common network name")
	cmd.Flags().StringVar(&f.subsidiary, "subsidiary", "", "Subsidiary ID")
	cmd.Flags().StringVar(&f.addressType, "address-type", "address", "Address type: address or hd")
	cmd.Flags().Int64Var(&f.syncStartDateSEC, "sync-start-sec", 0, "Optional earliest sync time as Unix seconds")
	cmd.Flags().StringVar(&f.viewKey, "view-key", "", "Optional network view key (for example Aleo)")
	cmd.Flags().BoolVar(&f.balanceMonitoringOnly, "balance-monitoring-only", false, "Create as a balance-monitoring-only wallet")
	cmd.Flags().BoolVar(&f.allowDuplicate, "allow-duplicate", false, "Create even if the same network/address already exists")
	cmd.Flags().IntVar(&f.concurrency, "concurrency", 8, "Maximum concurrent wallet creations")
	return cmd
}

func runOrgWalletsAdd(cmd *op.Call, f orgWalletAddFlags) error {
	operation := "add-organization-wallets"
	inputs, err := loadOrgWalletInputs(cmd.Context(), f, cmd.InOrStdin())
	if err != nil {
		return mutationError(cmd, operation, f.jsonOutput, err)
	}
	for i := range inputs {
		if err := normalizeAndValidateOrgWallet(&inputs[i]); err != nil {
			return mutationError(cmd, operation, f.jsonOutput, fmt.Errorf("wallet %d: %w", i+1, err))
		}
	}
	if f.concurrency < 1 || f.concurrency > 50 {
		return mutationError(cmd, operation, f.jsonOutput, op.NewValidationError("--concurrency must be between 1 and 50"))
	}
	orgID, err := resolveReportOrg(cmd.Context(), f.orgID)
	if err != nil {
		return mutationError(cmd, operation, f.jsonOutput, err)
	}
	requests := make([]map[string]any, 0, len(inputs))
	for _, input := range inputs {
		requests = append(requests, buildOrgWalletPayload(input))
	}
	preview := map[string]any{"method": "POST", "url": "organization GraphQL createWallet", "wallets": requests}
	if f.dryRun {
		return writeJSON(cmd.OutOrStdout(), mutationEnvelope{SchemaVersion: "1", Status: "preview", Operation: operation, Organization: orgID, DryRun: true, Request: preview})
	}
	if !f.yes {
		return mutationError(cmd, operation, f.jsonOutput, op.NewValidationError("refusing to change the organization without --yes (use --dry-run to preview)"))
	}

	// Resolve the token once before fan-out. Refreshing and rewriting the same
	// credential file in every worker would serialize the fast path.
	token, err := makeOrgTokenResolver(cmd.Context(), orgID)()
	if err != nil {
		return mutationError(cmd, operation, f.jsonOutput, fmt.Errorf("resolve organization token: %w", err))
	}
	client := newReportsClient(cmd.Context(), orgID)
	client.TokenResolver = func() (string, error) { return token, nil }
	if err := resolveWalletSubsidiaries(cmd, client, orgID, inputs); err != nil {
		return mutationError(cmd, operation, f.jsonOutput, err)
	}
	// A defaulted subsidiary changes the payload the preview showed.
	for i := range inputs {
		requests[i] = buildOrgWalletPayload(inputs[i])
	}
	existing, err := client.Wallets(cmd.Context(), orgID)
	if err != nil {
		return mutationError(cmd, operation, f.jsonOutput, fmt.Errorf("check existing wallets: %w", err))
	}
	results := make([]map[string]any, len(inputs))
	failures := make([]error, len(inputs))
	jobs := make(chan int)
	workerCount := min(f.concurrency, len(inputs))
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				input := inputs[i]
				wallet, createErr := client.CreateOrgWallet(cmd.Context(), orgID, requests[i], nil)
				if createErr != nil {
					failures[i] = createErr
					results[i] = map[string]any{"status": "failed", "input": input, "error": createErr.Error()}
					continue
				}
				results[i] = map[string]any{"status": "created", "input": input, "wallet": wallet}
			}
		}()
	}
	for i, input := range inputs {
		if !f.allowDuplicate {
			if wallet := findExistingOrgWallet(existing, input); wallet != nil {
				results[i] = map[string]any{"status": "skipped_existing", "input": input, "wallet": wallet}
				continue
			}
			for earlier := 0; earlier < i; earlier++ {
				if sameOrgWalletInput(inputs[earlier], input) {
					results[i] = map[string]any{"status": "skipped_duplicate_input", "input": input, "duplicateOf": earlier + 1}
					break
				}
			}
			if results[i] != nil {
				continue
			}
		}
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	created, skipped, failed := 0, 0, 0
	for _, result := range results {
		if result["status"] == "created" {
			created++
		} else if result["status"] == "skipped_existing" || result["status"] == "skipped_duplicate_input" {
			skipped++
		} else if result["status"] == "failed" {
			failed++
		}
	}
	status := "success"
	if failed > 0 {
		status = "partial_failure"
	}
	syncGuidance := organizationWalletSyncGuidance()
	envelope := mutationEnvelope{SchemaVersion: "1", Status: status, Operation: operation, Organization: orgID, Result: map[string]any{"created": created, "skipped": skipped, "failed": failed, "concurrency": workerCount, "wallets": results, "syncGuidance": syncGuidance}}
	if failed > 0 {
		_ = writeJSON(cmd.OutOrStdout(), envelope)
		batch := &op.BatchError{Summary: fmt.Sprintf("organization wallets: %d created, %d skipped, %d failed", created, skipped, failed), Created: created, Skipped: skipped}
		for _, failure := range failures {
			if failure != nil {
				batch.Failures = append(batch.Failures, failure)
			}
		}
		return batch
	}
	human := fmt.Sprintf("organization wallets: %d created, %d skipped\n%s\nCheck progress: bitwave transaction search --wallet WALLET_NAME --limit 1 --json\n", created, skipped, organizationWalletSyncExpectation)
	return outputMutation(cmd, f.jsonOutput, envelope, human)
}

func organizationWalletSyncGuidance() map[string]any {
	return map[string]any{
		"expectedDuration": "15 minutes to 24 hours",
		"dependsOn":        []string{"transaction history volume", "network load"},
		"message":          organizationWalletSyncExpectation,
		"checkCommand":     "bitwave transaction search --wallet WALLET_NAME --limit 1 --json",
	}
}

func loadOrgWalletInputs(ctx context.Context, f orgWalletAddFlags, stdin io.Reader) ([]orgWalletInput, error) {
	if f.input == "" {
		return []orgWalletInput{{Name: f.name, Type: f.walletType, Address: f.address, VaultAddress: f.vaultAddress, Protocol: f.protocol, NetworkID: f.network, SubsidiaryID: f.subsidiary, AddressType: f.addressType, SyncStartDateSEC: f.syncStartDateSEC, ViewKey: f.viewKey, IsBalanceMonitoringOnly: f.balanceMonitoringOnly}}, nil
	}
	var data []byte
	var err error
	if f.input == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = op.RuntimeFrom(ctx).Files.ReadFile(f.input)
	}
	if err != nil {
		return nil, fmt.Errorf("read wallet input: %w", err)
	}
	var inputs []orgWalletInput
	if err = json.Unmarshal(data, &inputs); err == nil {
		return inputs, nil
	}
	var wrapped struct {
		Wallets []orgWalletInput `json:"wallets"`
	}
	if wrapErr := json.Unmarshal(data, &wrapped); wrapErr != nil {
		return nil, fmt.Errorf("decode wallet input JSON: %w", err)
	}
	return wrapped.Wallets, nil
}

func normalizeAndValidateOrgWallet(input *orgWalletInput) error {
	input.Name = strings.TrimSpace(input.Name)
	input.Address = strings.TrimSpace(input.Address)
	input.NetworkID = strings.ToLower(strings.TrimSpace(input.NetworkID))
	input.SubsidiaryID = strings.TrimSpace(input.SubsidiaryID)
	input.AddressType = strings.ToLower(strings.TrimSpace(input.AddressType))
	input.Type = strings.ToLower(strings.TrimSpace(input.Type))
	input.VaultAddress = strings.TrimSpace(input.VaultAddress)
	input.Protocol = strings.TrimSpace(input.Protocol)
	if alias := organizationWalletNetworkAliases[input.NetworkID]; alias != "" {
		input.NetworkID = alias
	}
	// Every refusal below is static text: hosted adapters may show it as is.
	if input.Address == "" {
		return op.NewValidationError("address is required")
	}
	if input.NetworkID == "" {
		return op.NewValidationError("networkId is required")
	}
	if input.Type == "" {
		input.Type = orgWalletTypeBlockchain
	}
	if input.Type != orgWalletTypeBlockchain && input.Type != orgWalletTypeDefi {
		return op.NewValidationError("type must be " + orgWalletTypeBlockchain + " or " + orgWalletTypeDefi)
	}
	if input.AddressType == "" {
		input.AddressType = "address"
	}
	if input.AddressType != "address" && input.AddressType != "hd" {
		return op.NewValidationError("addressType must be address or hd")
	}
	if input.Type == orgWalletTypeDefi {
		if input.VaultAddress == "" {
			return op.NewValidationError("vaultAddress is required for defi wallets")
		}
		if strings.Contains(input.VaultAddress, " ") {
			return op.NewValidationError("vaultAddress cannot contain spaces")
		}
		if input.AddressType != "address" {
			return op.NewValidationError("defi wallets require addressType address")
		}
		if input.ViewKey != "" {
			return op.NewValidationError("defi wallets do not accept viewKey")
		}
	} else if input.VaultAddress != "" || input.Protocol != "" {
		return op.NewValidationError("vaultAddress and protocol require type defi")
	}
	if input.AddressType == "hd" && input.NetworkID != "btc" && input.NetworkID != "dash" {
		return fmt.Errorf("network %s: %w", input.NetworkID, op.NewValidationError("HD wallets are supported only for btc and dash"))
	}
	if input.SyncStartDateSEC < 0 {
		return op.NewValidationError("syncStartDateSEC cannot be negative")
	}
	if strings.TrimSpace(input.Description) != "" {
		return op.NewValidationError("the Bitwave WalletInput contract does not accept description during creation")
	}
	if input.Name == "" {
		input.Name = defaultOrgWalletName(*input)
	}
	return nil
}

// defaultOrgWalletName names a wallet the caller did not name after its
// network and a shortened address, e.g. "Ethereum 0xfc48...1ee3". Callers
// such as chat agents often omit the name; refusing would fail the whole add
// over a label the user can rename later.
func defaultOrgWalletName(input orgWalletInput) string {
	network := organizationWalletNetworks[input.NetworkID]
	if network == "" {
		network = strings.ToUpper(input.NetworkID)
	}
	if input.Type == orgWalletTypeDefi {
		network += " DeFi"
	}
	address := input.Address
	if len(address) > 12 {
		address = address[:6] + "..." + address[len(address)-4:]
	}
	return network + " " + address
}

func buildOrgWalletPayload(input orgWalletInput) map[string]any {
	var wallet map[string]any
	if input.Type == orgWalletTypeDefi {
		defi := map[string]any{"networkId": input.NetworkID, "walletAddress": input.Address, "vaultAddress": input.VaultAddress, "isSyncEnabled": true}
		if input.Protocol != "" {
			defi["protocol"] = input.Protocol
		}
		wallet = map[string]any{"name": input.Name, "type": orgWalletTypeDefi, "defi": defi}
	} else if input.AddressType == "hd" {
		wallet = map[string]any{"name": input.Name, "type": "watch", "watch": map[string]any{"coin": strings.ToUpper(input.NetworkID), "type": "hd", "derivationKey": input.Address}}
	} else {
		blockchain := map[string]any{"address": input.Address, "networkId": input.NetworkID}
		if input.ViewKey != "" {
			blockchain["viewKey"] = input.ViewKey
		}
		if len(input.Metadata) > 0 {
			blockchain["metadata"] = input.Metadata
		}
		wallet = map[string]any{"name": input.Name, "type": "accountBasedBlockchain", "accountBasedBlockchain": blockchain}
		if input.NetworkID == "canton" {
			wallet["structuredSyncerVersionConfig"] = map[string]any{"canton-sync-svc": map[string]any{"canton-incremental-sync-workflow": []map[string]int{{"version": 3}}, "canton-full-sync-workflow": []map[string]int{{"version": 3}}}}
		}
	}
	if input.SubsidiaryID != "" {
		wallet["subsidiaryId"] = input.SubsidiaryID
	}
	if input.SyncStartDateSEC > 0 {
		wallet["flags"] = map[string]any{"syncStartDateSEC": input.SyncStartDateSEC}
	}
	if input.IsBalanceMonitoringOnly {
		wallet["isBalanceMonitoringOnly"] = true
	}
	return wallet
}

// resolveWalletSubsidiaries checks every requested subsidiary exists and gives
// wallets without one the organization's only subsidiary. An organization that
// uses subsidiaries refuses a wallet without one (unless wallet rules assign
// it), and with a single subsidiary there is no choice to make. With several,
// the caller must pick; the platform stays the authority on whether one is
// required.
func resolveWalletSubsidiaries(cmd *op.Call, client *orgreports.Client, orgID string, inputs []orgWalletInput) error {
	wanted := map[string]bool{}
	for _, input := range inputs {
		if input.SubsidiaryID != "" {
			wanted[input.SubsidiaryID] = true
		}
	}
	subsidiaries, err := client.Subsidiaries(cmd.Context(), orgID)
	if err != nil {
		if len(wanted) == 0 {
			// Nothing to validate, and no default is better than a failed add.
			return nil
		}
		return fmt.Errorf("validate subsidiaries: %w", err)
	}
	for _, subsidiary := range subsidiaries {
		delete(wanted, subsidiary.ID)
	}
	if len(wanted) > 0 {
		missing := make([]string, 0, len(wanted))
		for id := range wanted {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		return fmt.Errorf("unknown subsidiary ID(s) %s: %w", strings.Join(missing, ", "), op.NewValidationError("subsidiary is not one of this organization's subsidiaries"))
	}
	if len(subsidiaries) == 1 {
		for i := range inputs {
			if inputs[i].SubsidiaryID == "" {
				inputs[i].SubsidiaryID = subsidiaries[0].ID
			}
		}
	}
	return nil
}

func findExistingOrgWallet(wallets []orgreports.Wallet, input orgWalletInput) *orgreports.Wallet {
	for i := range wallets {
		wallet := &wallets[i]
		if !strings.EqualFold(wallet.NetworkID, input.NetworkID) {
			continue
		}
		// A DeFi position is identified by (wallet, vault): the same wallet
		// can legitimately hold positions in several pools, and a plain
		// blockchain wallet for the address is not the same source.
		if input.Type == orgWalletTypeDefi || wallet.VaultAddress != "" {
			if !strings.EqualFold(strings.TrimSpace(wallet.VaultAddress), input.VaultAddress) {
				continue
			}
		}
		addresses := append([]string{wallet.Address}, wallet.Addresses...)
		for _, address := range addresses {
			if strings.EqualFold(strings.TrimSpace(address), input.Address) {
				return wallet
			}
		}
	}
	return nil
}

func sameOrgWalletInput(a, b orgWalletInput) bool {
	return strings.EqualFold(a.NetworkID, b.NetworkID) && strings.EqualFold(a.Address, b.Address) && strings.EqualFold(a.VaultAddress, b.VaultAddress)
}
