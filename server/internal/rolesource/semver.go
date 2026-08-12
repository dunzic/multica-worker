package rolesource

import (
	"strconv"
	"strings"
)

type semanticVersion struct {
	major, minor, patch uint64
	prerelease          []string
}

func parseSemanticVersion(raw string) (semanticVersion, bool) {
	if build := strings.IndexByte(raw, '+'); build >= 0 {
		if !validSemanticIdentifiers(raw[build+1:], false) {
			return semanticVersion{}, false
		}
		raw = raw[:build]
	}
	var prerelease []string
	if dash := strings.IndexByte(raw, '-'); dash >= 0 {
		if dash == len(raw)-1 {
			return semanticVersion{}, false
		}
		prereleaseText := raw[dash+1:]
		if !validSemanticIdentifiers(prereleaseText, true) {
			return semanticVersion{}, false
		}
		prerelease = strings.Split(prereleaseText, ".")
		raw = raw[:dash]
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return semanticVersion{}, false
	}
	values := [3]uint64{}
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return semanticVersion{}, false
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return semanticVersion{}, false
		}
		values[index] = value
	}
	return semanticVersion{major: values[0], minor: values[1], patch: values[2], prerelease: prerelease}, true
}

func validSemanticIdentifiers(raw string, rejectNumericLeadingZero bool) bool {
	if raw == "" {
		return false
	}
	for _, identifier := range strings.Split(raw, ".") {
		if identifier == "" {
			return false
		}
		numeric := true
		for _, char := range identifier {
			if (char < '0' || char > '9') && (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') && char != '-' {
				return false
			}
			if char < '0' || char > '9' {
				numeric = false
			}
		}
		if rejectNumericLeadingZero && numeric && len(identifier) > 1 && identifier[0] == '0' {
			return false
		}
	}
	return true
}

func compareSemanticVersion(left, right semanticVersion) int {
	for _, pair := range [][2]uint64{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(left.prerelease) == 0 && len(right.prerelease) == 0 {
		return 0
	}
	if len(left.prerelease) == 0 {
		return 1
	}
	if len(right.prerelease) == 0 {
		return -1
	}
	limit := min(len(left.prerelease), len(right.prerelease))
	for index := 0; index < limit; index++ {
		leftID, rightID := left.prerelease[index], right.prerelease[index]
		leftNumber, leftErr := strconv.ParseUint(leftID, 10, 64)
		rightNumber, rightErr := strconv.ParseUint(rightID, 10, 64)
		switch {
		case leftErr == nil && rightErr == nil:
			if leftNumber < rightNumber {
				return -1
			}
			if leftNumber > rightNumber {
				return 1
			}
		case leftErr == nil:
			return -1
		case rightErr == nil:
			return 1
		default:
			if leftID < rightID {
				return -1
			}
			if leftID > rightID {
				return 1
			}
		}
	}
	if len(left.prerelease) < len(right.prerelease) {
		return -1
	}
	if len(left.prerelease) > len(right.prerelease) {
		return 1
	}
	return 0
}

func semanticVersionSatisfies(versionText, constraintText string) bool {
	version, ok := parseSemanticVersion(versionText)
	if !ok {
		return false
	}
	operator := "="
	baseText := constraintText
	for _, candidate := range []string{">=", "<=", ">", "<", "^", "~"} {
		if strings.HasPrefix(constraintText, candidate) {
			operator = candidate
			baseText = strings.TrimPrefix(constraintText, candidate)
			break
		}
	}
	base, ok := parseSemanticVersion(baseText)
	if !ok {
		return false
	}
	comparison := compareSemanticVersion(version, base)
	// Range constraints do not silently opt into preview builds. A range that
	// explicitly names a prerelease may resolve another prerelease only on the
	// same core version; exact constraints keep normal SemVer precedence.
	if operator != "=" && len(version.prerelease) > 0 {
		if len(base.prerelease) == 0 || version.major != base.major || version.minor != base.minor || version.patch != base.patch {
			return false
		}
	}
	switch operator {
	case "=":
		return comparison == 0
	case ">=":
		return comparison >= 0
	case ">":
		return comparison > 0
	case "<=":
		return comparison <= 0
	case "<":
		return comparison < 0
	case "~":
		return comparison >= 0 && version.major == base.major && version.minor == base.minor
	case "^":
		if comparison < 0 {
			return false
		}
		switch {
		case base.major > 0:
			return version.major == base.major
		case base.minor > 0:
			return version.major == 0 && version.minor == base.minor
		default:
			return version.major == 0 && version.minor == 0 && version.patch == base.patch
		}
	default:
		return false
	}
}
