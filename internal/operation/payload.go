package operation

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// PayloadType is the JSON type a payload field must have. Types are strict: a
// quoted boolean is not a boolean and a number is not a date.
type PayloadType string

const (
	PayloadString  PayloadType = "string"
	PayloadBoolean PayloadType = "boolean"
	PayloadInteger PayloadType = "integer"
	// PayloadDate is a calendar date written YYYY-MM-DD.
	PayloadDate PayloadType = "date"
)

// PayloadField describes one allowed key of a request payload.
type PayloadField struct {
	Name        string
	Type        PayloadType
	Description string
	Required    bool
	Enum        []string
	// Hint is appended to a failure for this field. Developer-authored only.
	Hint string
}

// Payload is the contract for the JSON object an operation accepts. The CLI
// validates against it before anything is previewed or submitted, and hosted
// adapters publish the same contract as JSON Schema, so both agree.
type Payload struct {
	Description string
	Fields      []PayloadField
	// Check enforces rules across fields once every field is individually valid.
	// It returns a static, developer-authored message, or "" when satisfied.
	Check func(values map[string]any) string
}

// Validate returns nil when input satisfies the contract. Otherwise it returns
// a *ValidationError naming every problem so a caller can fix them in one try.
// Messages contain field names and expected shapes, never submitted values.
func (p *Payload) Validate(input map[string]any) error {
	if p == nil {
		return nil
	}
	known := make(map[string]PayloadField, len(p.Fields))
	for _, f := range p.Fields {
		known[f.Name] = f
	}
	var problems []string
	unknown := make([]string, 0)
	for key := range input {
		if _, ok := known[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	for _, key := range unknown {
		problems = append(problems, fmt.Sprintf("%q is not a supported field%s", safeFieldName(key), p.suggest(key)))
	}
	for _, f := range p.Fields {
		value, present := input[f.Name]
		if !present || value == nil {
			if f.Required {
				problems = append(problems, f.Name+" is required"+hintSuffix(f))
			}
			continue
		}
		if problem := checkValue(f, value); problem != "" {
			problems = append(problems, problem+hintSuffix(f))
		}
	}
	if len(problems) == 0 && p.Check != nil {
		if problem := p.Check(input); problem != "" {
			problems = append(problems, problem)
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return &ValidationError{message: "invalid request payload: " + strings.Join(problems, "; ") + ". Nothing was submitted."}
}

func hintSuffix(f PayloadField) string {
	if f.Hint == "" {
		return ""
	}
	return " (" + f.Hint + ")"
}

func checkValue(f PayloadField, value any) string {
	switch f.Type {
	case PayloadBoolean:
		if _, ok := value.(bool); !ok {
			return f.Name + " must be a JSON boolean (true or false), not a string or number"
		}
	case PayloadInteger:
		number, ok := value.(json.Number)
		if !ok {
			return f.Name + " must be a JSON integer"
		}
		if _, err := number.Int64(); err != nil {
			return f.Name + " must be a JSON integer"
		}
	case PayloadDate:
		text, ok := value.(string)
		if !ok {
			return f.Name + " must be a calendar date string written YYYY-MM-DD"
		}
		if _, err := time.Parse("2006-01-02", text); err != nil {
			return f.Name + " must be a real calendar date written YYYY-MM-DD"
		}
	default:
		text, ok := value.(string)
		if !ok {
			return f.Name + " must be a string"
		}
		if strings.TrimSpace(text) == "" {
			return f.Name + " must not be empty"
		}
		if len(f.Enum) > 0 && !contains(f.Enum, text) {
			return f.Name + " must be one of: " + strings.Join(f.Enum, ", ")
		}
	}
	return ""
}

// suggest points at a supported field whose name is close to a rejected one,
// such as walletIds for walletId.
func (p *Payload) suggest(key string) string {
	lower := strings.ToLower(key)
	for _, f := range p.Fields {
		name := strings.ToLower(f.Name)
		if strings.HasPrefix(lower, name) || strings.HasPrefix(name, lower) {
			return "; use " + f.Name
		}
	}
	return ""
}

func safeFieldName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if b.Len() >= 48 {
			b.WriteString("...")
			break
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('?')
		}
	}
	return b.String()
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// JSONSchema renders the contract for hosted tool schemas. Unknown keys are
// rejected, matching Validate.
func (p *Payload) JSONSchema() map[string]any {
	if p == nil {
		return nil
	}
	properties := make(map[string]any, len(p.Fields))
	var required []string
	for _, f := range p.Fields {
		property := map[string]any{"description": f.Description}
		switch f.Type {
		case PayloadDate:
			property["type"] = "string"
			property["pattern"] = `^\d{4}-\d{2}-\d{2}$`
			property["format"] = "date"
		case PayloadBoolean, PayloadInteger:
			property["type"] = string(f.Type)
		default:
			property["type"] = "string"
			property["minLength"] = 1
			if len(f.Enum) > 0 {
				property["enum"] = append([]string(nil), f.Enum...)
			}
		}
		properties[f.Name] = property
		if f.Required {
			required = append(required, f.Name)
		}
	}
	sort.Strings(required)
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if p.Description != "" {
		schema["description"] = p.Description
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
