# Organization categorization rules

`bitwave rule` is a direct interface to Bitwave's organization rule APIs. It
supports discovery, complete JSON create/update requests, validation, status
changes, deletion, and asynchronous execution. Rule interpretation and
selection remain the caller's responsibility.

## Commands

```bash
bitwave rule list
bitwave rule get RULE_ID
bitwave rule create --input rule.json
bitwave rule update RULE_ID --input rule.json
bitwave rule validate RULE_ID TRANSACTION_ID
bitwave rule enable RULE_ID
bitwave rule disable RULE_ID
bitwave rule delete RULE_ID
bitwave rule run
bitwave rule bulk-run --from-date YYYY-MM-DD --to-date YYYY-MM-DD
```

`rules` is accepted as an alias for `rule`.

## List and get

The list command returns compact summaries and a maximum of 25 enabled rules by
default:

```bash
bitwave --quiet rule list --org ORG_ID --query ETH --json
bitwave --quiet rule get RULE_ID --org ORG_ID --json
```

List options:

```text
--query TEXT         name, ID, asset, wallet, address, direction, or action
--limit NUMBER       1 through 500
--include-disabled   include disabled rules
--full               return complete Bitwave rule objects
```

The reports GraphQL list schema does not expose a server-side text filter, so
`--query` filters the returned rules locally. `get` uses the direct single-rule
endpoint.

## Create and update

The CLI accepts the complete Bitwave `Rule` JSON input rather than translating
it into a reduced set of flags:

```bash
bitwave rule create --input rule.json --dry-run --json
bitwave rule create --input rule.json --yes --json
bitwave rule update RULE_ID --input rule.json --dry-run --json
bitwave rule update RULE_ID --input rule.json --yes --json
```

For create, exactly one top-level rule variant is required. Missing `disabled`
is changed to `true`; creating an enabled rule requires explicit `--enabled`.
All mutations require `--yes` and support `--dry-run`.

Backend operations:

```text
POST https://api4.bitwave.io/graphql-reports  # list
POST https://api-app.bitwave.io/graphql       # create/update/status/delete
```

## Validate and execute

```bash
bitwave rule validate RULE_ID TRANSACTION_ID --json
bitwave rule run --yes --json
bitwave rule bulk-run \
  --from-date 2026-01-01 --to-date 2026-06-30 \
  --yes --json
```

`run` starts the organization's standard rules run, the same run the app's
Run rules button starts: every enabled rule over the transactions that still
need categorization. It asks sync-coordinator to invoke the organization's
`rules-workflow` job with `overrideHeartbeat=true`, so an explicit request
starts a run even shortly after the last one finished, and it needs no feature
flag. The result carries the job run id.

```text
POST https://api.bitwave.io/v3/orgs/ORG_ID/jobs/api-svc-rules/rules-workflow/ORG_ID.rules-workflow/invoke?overrideHeartbeat=true
```

The GraphQL `runRulesForOrg` mutation is no longer used: on some hosts it is
served by a legacy api-svc with the rules engine disabled, which accepts the
mutation and never runs anything.

`bulk-run` runs enabled rules over an inclusive date range in the
organization's timezone. Its default start is `2000-01-01`; its default end is
the current organization date. It is only available to organizations with
parallel rule processing enabled; otherwise the API answers 403
`feature_not_enabled` and the CLI says so and points at `rule run`.

Both commands confirm submission, not completion. Measure a run's effect by
re-reading the uncategorized transaction count once it has had time to finish.
