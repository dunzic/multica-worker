package rolesource

import "testing"

func TestSemanticVersionSatisfies(t *testing.T) {
	tests := []struct {
		version, constraint string
		want                bool
	}{
		{"1.2.3", "1.2.3", true},
		{"1.2.4", "1.2.3", false},
		{"1.9.0", "^1.2.3", true},
		{"2.0.0", "^1.2.3", false},
		{"0.3.1", "^0.3.0", true},
		{"0.4.0", "^0.3.0", false},
		{"1.2.9", "~1.2.3", true},
		{"1.3.0", "~1.2.3", false},
		{"1.2.3", ">=1.2.3", true},
		{"1.2.3-beta.2", ">1.2.3-beta.1", true},
		{"1.3.0-beta.1", "^1.2.3", false},
		{"1.2.2", "<1.2.3", true},
		{"1.2.3", "<=1.2.3", true},
		{"1.2", "^1.2.0", false},
		{"1.2.3", "", false},
	}
	for _, test := range tests {
		t.Run(test.version+"_"+test.constraint, func(t *testing.T) {
			if got := semanticVersionSatisfies(test.version, test.constraint); got != test.want {
				t.Fatalf("semanticVersionSatisfies(%q, %q) = %v, want %v", test.version, test.constraint, got, test.want)
			}
		})
	}
}

func TestParseSemanticVersionRejectsInvalidIdentifiers(t *testing.T) {
	for _, version := range []string{
		"1.0.0-01",
		"1.0.0-alpha_beta",
		"1.0.0-alpha..beta",
		"1.0.0+",
		"1.0.0+build_meta",
	} {
		if _, ok := parseSemanticVersion(version); ok {
			t.Fatalf("parseSemanticVersion(%q) succeeded", version)
		}
	}
}
