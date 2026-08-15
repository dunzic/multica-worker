package agentwaker

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"regexp"
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/internal/rolesource"
)

var (
	envLinePattern    = regexp.MustCompile("^\\s*(?:export\\s+)?([A-Za-z_][A-Za-z0-9_]*)\\s*=\\s*(.*)$")
	secretNamePattern = regexp.MustCompile("(?i)(secret|token|key|password|credential|access_token)")
	envRefPattern     = regexp.MustCompile("\\$\\{([A-Za-z_][A-Za-z0-9_]*)\\}")
)

type parsedEnv struct {
	order        []string
	descriptions map[string]string
	values       map[string]string
}

func parseEnv(data []byte) parsedEnv {
	out := parsedEnv{descriptions: map[string]string{}, values: map[string]string{}}
	pendingDescription := ""
	for _, raw := range bytes.Split(data, []byte("\n")) {
		line := strings.TrimRight(string(raw), "\r")
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			pendingDescription = ""
			continue
		case strings.HasPrefix(trimmed, "#"):
			comment := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
			if pendingDescription == "" {
				pendingDescription = comment
			} else {
				pendingDescription += "\n" + comment
			}
			continue
		}
		match := envLinePattern.FindStringSubmatch(line)
		if match == nil {
			pendingDescription = ""
			continue
		}
		name := match[1]
		if _, exists := out.values[name]; !exists {
			out.order = append(out.order, name)
		}
		out.descriptions[name] = pendingDescription
		out.values[name] = unquote(stripInlineComment(match[2]))
		pendingDescription = ""
	}
	return out
}

func mergeEnvironment(example, actual parsedEnv, digestKey []byte) []rolesource.EnvironmentKey {
	names := append([]string(nil), example.order...)
	seen := make(map[string]bool, len(names)+len(actual.order))
	for _, name := range names {
		seen[name] = true
	}
	for _, name := range actual.order {
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	out := make([]rolesource.EnvironmentKey, 0, len(names))
	for _, name := range names {
		value, configured := actual.values[name]
		entry := rolesource.EnvironmentKey{
			Name:        name,
			Description: example.descriptions[name],
			Secret:      secretNamePattern.MatchString(name),
			Configured:  configured,
		}
		if configured {
			entry.ValueDigest = envValueDigest(name, value, digestKey)
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func envValueDigest(name, value string, key []byte) string {
	mac := hmac.New(sha256.New, key)
	hashPart(mac, "agentwaker-env-digest-v1")
	hashPart(mac, name)
	hashPart(mac, value)
	return "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
}

func hashPart(target hash.Hash, value string) {
	_, _ = target.Write([]byte{0})
	_, _ = target.Write([]byte(value))
}

func stripInlineComment(value string) string {
	inSingle, inDouble := false, false
	for index := 0; index < len(value); index++ {
		switch value[index] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble && index > 0 && value[index-1] == ' ' {
				return strings.TrimRight(value[:index], " \t")
			}
		}
	}
	return value
}

func unquote(value string) string {
	if len(value) < 2 {
		return value
	}
	first, last := value[0], value[len(value)-1]
	if first == '"' && last == '"' || first == '\'' && last == '\'' {
		return value[1 : len(value)-1]
	}
	return value
}
