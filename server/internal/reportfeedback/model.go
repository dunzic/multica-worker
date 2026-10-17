// Package reportfeedback validates the platform-owned data beside a report.
// Uploaded HTML remains untrusted presentation, never an authority or data store.
package reportfeedback

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxFields = 20

type Field struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Options  []string `json:"options"`
}

var fieldID = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func ValidateFields(fields []Field) error {
	if len(fields) < 1 || len(fields) > MaxFields {
		return fmt.Errorf("configure between 1 and %d fields", MaxFields)
	}
	seen := map[string]bool{}
	for _, f := range fields {
		if !fieldID.MatchString(f.ID) || f.ID == "constructor" || f.ID == "prototype" || seen[f.ID] {
			return fmt.Errorf("invalid or duplicate field id")
		}
		seen[f.ID] = true
		if strings.TrimSpace(f.Label) == "" || utf8.RuneCountInString(f.Label) > 80 || strings.ContainsRune(f.Label, 0) {
			return fmt.Errorf("field label must contain 1-80 characters")
		}
		switch f.Type {
		case "text", "number", "date", "boolean":
			if len(f.Options) != 0 {
				return fmt.Errorf("only choice fields accept options")
			}
		case "select", "multi_select":
			if len(f.Options) < 1 || len(f.Options) > 50 {
				return fmt.Errorf("choice fields need 1-50 options")
			}
			options := map[string]bool{}
			for _, option := range f.Options {
				if strings.TrimSpace(option) == "" || utf8.RuneCountInString(option) > 80 || strings.ContainsRune(option, 0) || options[option] {
					return fmt.Errorf("invalid or duplicate choice option")
				}
				options[option] = true
			}
		default:
			return fmt.Errorf("unsupported field type")
		}
	}
	return nil
}

// Once records exist, schema edits cannot reinterpret or discard their values.
// Labels, order and additive optional fields/options remain editable.
func ValidateSchemaChange(before, after []Field, hasRows bool) error {
	if err := ValidateFields(after); err != nil {
		return err
	}
	if !hasRows {
		return nil
	}
	byID := map[string]Field{}
	for _, f := range after {
		byID[f.ID] = f
	}
	oldIDs := map[string]bool{}
	for _, old := range before {
		oldIDs[old.ID] = true
		next, ok := byID[old.ID]
		if !ok || next.Type != old.Type || (!old.Required && next.Required) {
			return fmt.Errorf("existing records prevent removing fields, changing types or adding required constraints")
		}
		for _, option := range old.Options {
			if !slices.Contains(next.Options, option) {
				return fmt.Errorf("existing records prevent removing choice options")
			}
		}
	}
	for _, f := range after {
		if !oldIDs[f.ID] && f.Required {
			return fmt.Errorf("new fields must be optional when records exist")
		}
	}
	return nil
}

func ValidateValues(fields []Field, values map[string]json.RawMessage) error {
	if values == nil {
		return fmt.Errorf("values must be an object")
	}
	known := map[string]bool{}
	for _, f := range fields {
		known[f.ID] = true
		raw, exists := values[f.ID]
		if !exists || strings.TrimSpace(string(raw)) == "null" {
			if f.Required {
				return fmt.Errorf("field %s is required", f.ID)
			}
			continue
		}
		bad := func() error { return fmt.Errorf("invalid value for field %s", f.ID) }
		switch f.Type {
		case "text", "date", "select":
			var s string
			if json.Unmarshal(raw, &s) != nil || utf8.RuneCountInString(s) > 4000 || strings.ContainsRune(s, 0) {
				return bad()
			}
			if s == "" && !f.Required {
				continue
			}
			if f.Required && strings.TrimSpace(s) == "" {
				return bad()
			}
			if f.Type == "date" {
				if parsed, err := time.Parse("2006-01-02", s); err != nil || parsed.Format("2006-01-02") != s {
					return bad()
				}
			}
			if f.Type == "select" && !slices.Contains(f.Options, s) {
				return bad()
			}
		case "number":
			var n float64
			if json.Unmarshal(raw, &n) != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > 1e15 {
				return bad()
			}
		case "boolean":
			var b bool
			if json.Unmarshal(raw, &b) != nil {
				return bad()
			}
		case "multi_select":
			var selected []string
			if json.Unmarshal(raw, &selected) != nil || len(selected) > len(f.Options) || (f.Required && len(selected) == 0) {
				return bad()
			}
			seen := map[string]bool{}
			for _, s := range selected {
				if !slices.Contains(f.Options, s) || seen[s] {
					return bad()
				}
				seen[s] = true
			}
		default:
			return bad()
		}
	}
	for key := range values {
		if !known[key] {
			return fmt.Errorf("unknown field id")
		}
	}
	return nil
}
