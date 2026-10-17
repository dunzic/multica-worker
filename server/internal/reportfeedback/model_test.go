package reportfeedback

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestFieldsAndSchemaEvolution(t *testing.T) {
	base := []Field{{ID: "result", Label: "Result", Type: "select", Options: []string{"pending", "done"}}}
	if err := ValidateFields(base); err != nil {
		t.Fatal(err)
	}
	fields := []Field{}
	for i := 0; i < 20; i++ {
		fields = append(fields, Field{ID: fmt.Sprintf("field_%d", i), Label: "Field", Type: "text"})
	}
	if err := ValidateFields(fields); err != nil {
		t.Fatal(err)
	}
	fields = append(fields, Field{ID: "extra", Label: "Extra", Type: "text"})
	if ValidateFields(fields) == nil {
		t.Fatal("accepted 21 fields")
	}
	for _, id := range []string{"__proto__", "constructor", "prototype", "bad-key", ""} {
		if ValidateFields([]Field{{ID: id, Label: "Field", Type: "text"}}) == nil {
			t.Fatalf("accepted %s", id)
		}
	}
	if ValidateFields(append(base, base...)) == nil {
		t.Fatal("accepted duplicate id")
	}
	if ValidateSchemaChange(base, []Field{{ID: "result", Label: "Renamed", Type: "select", Options: []string{"pending", "done", "later"}}}, true) != nil {
		t.Fatal("safe additive edit rejected")
	}
	for _, next := range [][]Field{
		{{ID: "other", Label: "Other", Type: "text"}},
		{{ID: "result", Label: "Result", Type: "text"}},
		{{ID: "result", Label: "Result", Type: "select", Required: true, Options: []string{"pending", "done"}}},
		{{ID: "result", Label: "Result", Type: "select", Options: []string{"done"}}},
	} {
		if ValidateSchemaChange(base, next, true) == nil {
			t.Fatal("destructive schema change accepted")
		}
	}
}

func TestTypedValues(t *testing.T) {
	for _, tt := range []struct {
		kind      string
		good, bad []string
		options   []string
	}{
		{"text", []string{`"hello"`, `"<script>text</script>"`, `null`}, []string{`1`, `{}`}, nil},
		{"number", []string{`0`, `-2.5`, `null`}, []string{`"12"`, `true`, `1e100`}, nil},
		{"boolean", []string{`false`, `true`, `null`}, []string{`"true"`, `0`}, nil},
		{"date", []string{`"2028-02-29"`, `null`}, []string{`"2026-02-29"`, `"2026-1-1"`, `"yesterday"`}, nil},
		{"select", []string{`"done"`, `null`}, []string{`"missing"`, `[]`}, []string{"pending", "done"}},
		{"multi_select", []string{`["done"]`, `[]`, `null`}, []string{`["missing"]`, `["done","done"]`, `"done"`}, []string{"pending", "done"}},
	} {
		t.Run(tt.kind, func(t *testing.T) {
			f := []Field{{ID: "field", Label: "Field", Type: tt.kind, Options: tt.options}}
			for _, raw := range tt.good {
				if err := ValidateValues(f, map[string]json.RawMessage{"field": json.RawMessage(raw)}); err != nil {
					t.Fatalf("good %s: %v", raw, err)
				}
			}
			for _, raw := range tt.bad {
				if ValidateValues(f, map[string]json.RawMessage{"field": json.RawMessage(raw)}) == nil {
					t.Fatalf("accepted %s", raw)
				}
			}
		})
	}
	if ValidateValues([]Field{{ID: "required", Type: "boolean", Required: true}}, map[string]json.RawMessage{"required": json.RawMessage(`false`)}) != nil {
		t.Fatal("false is a valid required boolean")
	}
	if ValidateValues([]Field{{ID: "required", Type: "text", Required: true}}, map[string]json.RawMessage{}) == nil {
		t.Fatal("missing required value accepted")
	}
	if ValidateValues(nil, map[string]json.RawMessage{"extra": json.RawMessage(`1`)}) == nil {
		t.Fatal("unknown field accepted")
	}
	for _, kind := range []string{"text", "number", "boolean", "date", "select", "multi_select"} {
		if ValidateValues([]Field{{ID: "required", Type: kind, Required: true}}, map[string]json.RawMessage{"required": json.RawMessage(" null ")}) == nil {
			t.Fatalf("required %s accepted whitespace null", kind)
		}
	}
	if ValidateFields([]Field{{ID: "nul", Label: "bad\x00label", Type: "text"}}) == nil {
		t.Fatal("NUL field label accepted")
	}
}
