package provider

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

var keyCleaner = regexp.MustCompile(`[^A-Za-z0-9_]+`)

// SanitizeKey rewrites a raw key into one that is safe to use in Nomad's
// ${secret.<name>.<key>} interpolation syntax. Runs of characters outside
// [A-Za-z0-9_] collapse to a single "_", and leading/trailing underscores
// are trimmed. It mirrors the key rules used by the 1Password provider.
func SanitizeKey(s string) string {
	return strings.Trim(keyCleaner.ReplaceAllString(s, "_"), "_")
}

// ExpandJSON turns a fetched secret string into interpolation keys. When raw
// is a JSON object, each top-level key is sanitized into values[key] and the
// caller gets Object=true: scalar members are stringified, and nested
// objects/arrays are re-encoded as compact JSON. The raw string is always
// preserved at values["value"]. When raw is not a JSON object (a scalar,
// array, or invalid JSON), it returns ({"value": raw}, false) so plain
// SecureString/String parameters pass through untouched.
func ExpandJSON(raw string) (map[string]string, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return map[string]string{"value": raw}, false
	}

	values := map[string]string{"value": raw}
	for k, v := range obj {
		key := SanitizeKey(k)
		if key == "" || key == "value" {
			continue
		}
		if _, exists := values[key]; !exists {
			values[key] = stringifyJSON(v)
		}
	}
	return values, true
}

// stringifyJSON renders a JSON value as a string: strings unquote to their
// literal text, numbers/bools stringify to their scalar form, and nested
// objects/arrays are left as their compact JSON encoding.
func stringifyJSON(v json.RawMessage) string {
	trimmed := strings.TrimSpace(string(v))
	if trimmed == "" {
		return ""
	}

	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			return s
		}
	case 't', 'f', 'n':
		return trimmed // true / false / null
	case '{', '[':
		return trimmed // nested object/array: compact JSON
	default:
		// A JSON number: normalise its textual form.
		if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return strconv.FormatFloat(f, 'f', -1, 64)
		}
	}
	return trimmed
}
