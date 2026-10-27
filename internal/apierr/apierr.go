// Package apierr turns raw HTTP error responses from Bitwave services into
// messages a human can act on. Servers variously return JSON error envelopes,
// bare text, or whole HTML error pages (e.g. a gateway 404) — none of which
// should be dumped verbatim into a CLI error.
package apierr

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const maxDetailLen = 200

// Error is a structured API error. Callers can errors.As it to inspect the
// HTTP status; its message is the human-readable rendering.
type Error struct {
	Status int
	Method string
	URL    string
	Detail string
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("%s %s: %s", e.Method, e.URL, statusLine(e.Status))
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	if hint := hintFor(e.Status); hint != "" {
		msg += "\n  " + hint
	}
	return msg
}

// Format builds a readable error for a non-2xx response. method and url
// describe the request; body is the (possibly empty) response body.
func Format(status int, method, url string, body []byte) error {
	return &Error{Status: status, Method: method, URL: url, Detail: extractDetail(body)}
}

func statusLine(status int) string {
	text := http.StatusText(status)
	if text == "" {
		return fmt.Sprintf("HTTP %d", status)
	}
	return fmt.Sprintf("HTTP %d %s", status, text)
}

// extractDetail pulls a human-readable message out of the body: a JSON
// error envelope if present, plain text if short, nothing if it's HTML
// or noise.
func extractDetail(body []byte) string {
	s := strings.TrimSpace(string(body))
	if s == "" {
		return ""
	}
	// JSON envelope: try the common message fields.
	if strings.HasPrefix(s, "{") {
		var env struct {
			Error   string `json:"error"`
			Message string `json:"message"`
			Detail  string `json:"detail"`
			Errors  []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if json.Unmarshal(body, &env) == nil {
			for _, m := range []string{env.Message, env.Error, env.Detail} {
				if m != "" {
					return truncate(m)
				}
			}
			if len(env.Errors) > 0 && env.Errors[0].Message != "" {
				return truncate(env.Errors[0].Message)
			}
		}
		return "" // JSON with no recognizable message: omit rather than dump
	}
	// HTML error page (gateway/proxy default pages): never dump markup.
	if strings.HasPrefix(s, "<") {
		return ""
	}
	return truncate(s)
}

func truncate(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxDetailLen {
		return s[:maxDetailLen] + "…"
	}
	return s
}

func hintFor(status int) string {
	switch status {
	case http.StatusNotFound:
		return "the server doesn't expose this endpoint — it may predate this feature, or the base URL may be wrong (BITWAVE_BASE_URL_GL / --base-url)"
	case http.StatusUnauthorized:
		return "authentication required — run `bitwave auth login`, or set BITWAVE_TOKEN / BITWAVE_AGENT_TOKEN"
	case http.StatusForbidden:
		return "your identity lacks access to this resource — check the active org (`bitwave org current`)"
	default:
		return ""
	}
}

// GraphQLError is a GraphQL response that carried errors: the server answered
// and refused the request, so nothing about it is a transport failure. The
// messages come from the server and are untrusted.
type GraphQLError struct {
	Messages []string
}

func (e *GraphQLError) Error() string {
	if len(e.Messages) == 0 {
		return "GraphQL: the server returned an error without a message"
	}
	return "GraphQL: " + strings.Join(e.Messages, "; ")
}
