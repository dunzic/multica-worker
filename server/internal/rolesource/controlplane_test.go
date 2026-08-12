package rolesource

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCanonicalJSONObjectIsStableAndRejectsTrailingValues(t *testing.T) {
	first, err := canonicalJSONObject(json.RawMessage(`{"z":1,"a":{"b":true}}`), 1024)
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonicalJSONObject(json.RawMessage(` { "a": { "b": true }, "z": 1 } `), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("canonical JSON differs: %s != %s", first, second)
	}
	for _, invalid := range []string{`[]`, `null`, `{"ok":true} false`, `{"ok":true} 1`} {
		if _, err := canonicalJSONObject(json.RawMessage(invalid), 1024); err == nil {
			t.Fatalf("canonicalJSONObject accepted %s", invalid)
		}
	}
}

func TestConfigSummaryRejectsPathLikeAndDuplicateAttributes(t *testing.T) {
	pathLike := ConfigSummary{Configured: true, Attributes: []ConfigAttribute{{Name: "root_name", Value: "/private/source"}}}
	if err := validateConfigSummary(&pathLike); err == nil {
		t.Fatal("validateConfigSummary accepted an absolute path")
	}
	duplicate := ConfigSummary{Configured: true, Attributes: []ConfigAttribute{
		{Name: "root_name", Value: "one"},
		{Name: "root_name", Value: "two"},
	}}
	if err := validateConfigSummary(&duplicate); err == nil {
		t.Fatal("validateConfigSummary accepted duplicate attributes")
	}
}
