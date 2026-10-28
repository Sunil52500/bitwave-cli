package sdk

import (
	"github.com/bitwave-io/bitwave-cli/internal/apierr"
	"github.com/bitwave-io/bitwave-cli/internal/operation"
	"github.com/bitwave-io/bitwave-cli/internal/orgreports"
)

// APIError is a non-success HTTP API response. Both Invoke's error and the
// CLI adapter's CommandResult.Err preserve it through errors.As. Status is
// structured protocol data, not parsed out of terminal presentation text.
//
// URL, Detail and Error() remain untrusted and may contain sensitive upstream
// information. Hosted callers must apply their own disclosure policy; this
// alias does not designate arbitrary server messages as safe for publication.
type APIError = apierr.Error

// ValidationError is a local, caller-correctable validation failure with a
// static message safe for hosted adapters to display. Use errors.As and the
// matched ValidationError's Error(), not a wrapping error's text: wrappers may
// contain user input. Other error types, including APIError, remain untrusted.
type ValidationError = operation.ValidationError

// ConfirmationRequiredError is a mutation refused locally for want of --yes
// (yes: true). Nothing was sent; the same request with the confirmation is the
// correct next step. Its message is static and safe to display.
type ConfirmationRequiredError = operation.ConfirmationRequiredError

// GraphQLError is a Bitwave platform GraphQL response that carried errors: the
// server answered and refused the request. Messages remain untrusted.
type GraphQLError = apierr.GraphQLError

// BatchError reports a batch mutation (for example org wallets add) in which
// some items failed. Its Failures are reachable through errors.As, so a
// per-item GraphQLError or APIError can be inspected; their text is untrusted.
type BatchError = operation.BatchError

// BulkRulesNotEnabledDetail is the static APIError detail the SDK returns
// when rule bulk-run is refused with 403 feature_not_enabled, that is when the
// organization lacks parallel rule processing. Hosted adapters may publish it
// verbatim; it names the standard rules run as the alternative.
const BulkRulesNotEnabledDetail = orgreports.BulkRulesNotEnabledDetail
