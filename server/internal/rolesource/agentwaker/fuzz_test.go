package agentwaker

import (
	"strings"
	"testing"
)

func FuzzParseEnvProducesOnlyKeyedDigests(f *testing.F) {
	f.Add([]byte("API_TOKEN=secret\n"))
	f.Add([]byte("export A='quoted # value' # comment\n"))
	f.Add([]byte("DUPLICATE=first\nDUPLICATE=second\n"))
	f.Fuzz(func(t *testing.T, body []byte) {
		parsed := parseEnv(body)
		normalized := mergeEnvironment(parsedEnv{descriptions: map[string]string{}, values: map[string]string{}}, parsed, []byte("0123456789abcdef0123456789abcdef"))
		for _, entry := range normalized {
			if entry.Configured && !strings.HasPrefix(entry.ValueDigest, "hmac-sha256:") {
				t.Fatalf("configured key %q has unsafe digest %q", entry.Name, entry.ValueDigest)
			}
		}
	})
}

func FuzzJoinWithinNeverEscapesBase(f *testing.F) {
	f.Add("information-collection/CAPABILITY.yaml")
	f.Add("../writer/env/.env")
	f.Add("/etc/passwd")
	f.Add("C:\\secret")
	f.Fuzz(func(t *testing.T, relative string) {
		joined, err := joinWithin("capabilities", relative)
		if err != nil {
			return
		}
		if !strings.HasPrefix(joined, "capabilities/") || strings.Contains(joined, "\\") {
			t.Fatalf("joinWithin returned unsafe path %q for %q", joined, relative)
		}
	})
}

func FuzzCanonicalJSONRejectsTrailingValues(f *testing.F) {
	f.Add([]byte(`{"a":1}`))
	f.Add([]byte(`{"token":"value"} {"other":true}`))
	f.Add([]byte("[1,2,3]"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		canonical, err := canonicalJSON(raw)
		if err != nil {
			return
		}
		second, err := canonicalJSON(canonical)
		if err != nil {
			t.Fatalf("canonical JSON cannot be parsed again: %v", err)
		}
		if string(canonical) != string(second) {
			t.Fatalf("canonical JSON is not idempotent: %q != %q", canonical, second)
		}
	})
}
