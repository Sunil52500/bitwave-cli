package operation

// ValidationError is a local, caller-correctable validation failure whose
// message is safe for hosted adapters to display. Do not use it for upstream
// responses, arbitrary wrapped errors, or messages containing input values.
type ValidationError struct {
	message string
}

// NewValidationError must only be given a static, developer-authored message.
// Keeping the message private prevents external SDK users from accidentally
// labelling arbitrary provider errors as safe for disclosure.
func NewValidationError(message string) *ValidationError {
	return &ValidationError{message: message}
}

func (e *ValidationError) Error() string {
	if e == nil || e.message == "" {
		return "invalid operation arguments"
	}
	return e.message
}

// BatchError reports a batch mutation in which some items failed. Created and
// Skipped count the items that did not fail; Failures holds each failed item's
// error in input order and is reachable through errors.As (for example an
// APIError or GraphQLError). Like those, the failure text is untrusted.
type BatchError struct {
	Summary  string
	Created  int
	Skipped  int
	Failures []error
}

func (e *BatchError) Error() string {
	if len(e.Failures) == 0 {
		return e.Summary
	}
	return e.Summary + "; first failure: " + e.Failures[0].Error()
}

func (e *BatchError) Unwrap() []error { return e.Failures }
