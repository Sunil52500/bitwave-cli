package operation

import (
	"encoding/json"
	"strings"
	"testing"
)

func testPayload() *Payload {
	return &Payload{
		Fields: []PayloadField{
			{Name: "walletId", Type: PayloadString, Required: true, Description: "wallet"},
			{Name: "startSEC", Type: PayloadDate, Required: true, Description: "start"},
			{Name: "endSEC", Type: PayloadDate, Required: true, Description: "end"},
			{Name: "flag", Type: PayloadBoolean, Description: "flag"},
			{Name: "count", Type: PayloadInteger, Description: "count"},
			{Name: "kind", Type: PayloadString, Enum: []string{"a", "b"}, Description: "kind"},
		},
		Check: func(v map[string]any) string {
			if v["startSEC"].(string) > v["endSEC"].(string) {
				return "startSEC must not be after endSEC"
			}
			return ""
		},
	}
}

func valid() map[string]any {
	return map[string]any{"walletId": "w1", "startSEC": "2026-04-01", "endSEC": "2026-10-15"}
}

func TestPayloadAcceptsValidInput(t *testing.T) {
	input := valid()
	input["flag"] = true
	input["count"] = json.Number("3")
	input["kind"] = "a"
	if err := testPayload().Validate(input); err != nil {
		t.Fatal(err)
	}
}

func TestPayloadRejectsBadInputWithSafeMessages(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"missing wallet", func(m map[string]any) { delete(m, "walletId") }, "walletId is required"},
		{"null wallet", func(m map[string]any) { m["walletId"] = nil }, "walletId is required"},
		{"blank wallet", func(m map[string]any) { m["walletId"] = "  " }, "walletId must not be empty"},
		{"unknown field with suggestion", func(m map[string]any) { m["walletIds"] = []any{"w1"} }, `"walletIds" is not a supported field; use walletId`},
		{"unknown field", func(m map[string]any) { m["bogus"] = 1 }, `"bogus" is not a supported field`},
		{"quoted boolean", func(m map[string]any) { m["flag"] = "true" }, "flag must be a JSON boolean"},
		{"numeric date", func(m map[string]any) { m["startSEC"] = json.Number("1775000000") }, "startSEC must be a calendar date string"},
		{"impossible date", func(m map[string]any) { m["endSEC"] = "2026-02-30" }, "endSEC must be a real calendar date"},
		{"wrong date format", func(m map[string]any) { m["endSEC"] = "10/15/2026" }, "endSEC must be a real calendar date"},
		{"fractional integer", func(m map[string]any) { m["count"] = json.Number("1.5") }, "count must be a JSON integer"},
		{"bad enum", func(m map[string]any) { m["kind"] = "z" }, "kind must be one of: a, b"},
		{"reversed dates", func(m map[string]any) { m["startSEC"], m["endSEC"] = "2026-10-15", "2026-04-01" }, "startSEC must not be after endSEC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := valid()
			tc.mutate(input)
			err := testPayload().Validate(input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if _, ok := err.(*ValidationError); !ok {
				t.Fatalf("error type = %T, want *ValidationError", err)
			}
			if !strings.Contains(err.Error(), "Nothing was submitted") {
				t.Fatalf("error must say nothing was submitted: %v", err)
			}
		})
	}
}

func TestPayloadReportsEveryProblemAtOnce(t *testing.T) {
	err := testPayload().Validate(map[string]any{"walletIds": []any{"w1"}, "flag": "yes"})
	for _, want := range []string{"walletIds", "walletId is required", "startSEC is required", "flag must be a JSON boolean"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want %q", err, want)
		}
	}
}

func TestPayloadNeverEchoesSubmittedValues(t *testing.T) {
	input := valid()
	input["flag"] = "SECRET-VALUE"
	input["bad key\n<script>"] = "SECRET-VALUE"
	err := testPayload().Validate(input)
	if err == nil || strings.Contains(err.Error(), "SECRET-VALUE") || strings.ContainsAny(err.Error(), "\n<>") {
		t.Fatalf("message leaked input: %v", err)
	}
}

func TestNilPayloadAcceptsAnything(t *testing.T) {
	var p *Payload
	if err := p.Validate(map[string]any{"x": 1}); err != nil {
		t.Fatal(err)
	}
}

func TestPayloadJSONSchemaMatchesContract(t *testing.T) {
	schema := testPayload().JSONSchema()
	if schema["additionalProperties"] != false {
		t.Fatal("unknown fields must be rejected")
	}
	required := schema["required"].([]string)
	if strings.Join(required, ",") != "endSEC,startSEC,walletId" {
		t.Fatalf("required = %v", required)
	}
	start := schema["properties"].(map[string]any)["startSEC"].(map[string]any)
	if start["format"] != "date" || start["type"] != "string" {
		t.Fatalf("startSEC = %v", start)
	}
	if testPayload().JSONSchema()["properties"].(map[string]any)["flag"].(map[string]any)["type"] != "boolean" {
		t.Fatal("flag must be boolean")
	}
}
