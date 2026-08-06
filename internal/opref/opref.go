// Package opref parses 1Password secret references of the form
// op://<vault>/<item>[/<section>]/<field>. Vault and item segments accept
// either a name/title or a 1Password ID. The section and field segments are
// optional: a two-segment reference (op://vault/item) addresses the whole
// item, in which case every non-empty field is returned as a key/value pair.
package opref

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Ref is a parsed 1Password secret reference.
type Ref struct {
	Vault   string // vault name or ID (required)
	Item    string // item title or ID (required)
	Section string // section label or ID (optional, only with Field)
	Field   string // field label or ID (optional)

	// Attribute is the optional ?attribute= query parameter. The only
	// value 1Password defines today is "otp", which asks for the current
	// TOTP code of an OTP field instead of its stored value.
	Attribute string
}

// WholeItem reports whether the reference addresses an entire item rather
// than a single field.
func (r Ref) WholeItem() bool { return r.Field == "" }

// String reassembles the reference in canonical op:// form. Values are not
// re-escaped; this is intended for log/error messages only.
func (r Ref) String() string {
	parts := []string{r.Vault, r.Item}
	if r.Section != "" {
		parts = append(parts, r.Section)
	}
	if r.Field != "" {
		parts = append(parts, r.Field)
	}
	s := "op://" + strings.Join(parts, "/")
	if r.Attribute != "" {
		s += "?attribute=" + r.Attribute
	}
	return s
}

// Parse parses a secret reference. The leading "op://" scheme is optional so
// that plain "vault/item/field" paths also work in a Nomad secret block.
func Parse(raw string) (Ref, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Ref{}, fmt.Errorf("empty secret reference")
	}

	if rest, ok := strings.CutPrefix(s, "op://"); ok {
		s = rest
	} else if strings.Contains(s, "://") {
		scheme := s[:strings.Index(s, "://")]
		return Ref{}, fmt.Errorf("unsupported scheme %q: secret references must use op://", scheme)
	}

	var attribute string
	if s, attribute = splitQuery(s); attribute != "" && attribute != "otp" {
		return Ref{}, fmt.Errorf("unsupported attribute %q: only \"otp\" is supported", attribute)
	}

	segs := strings.Split(s, "/")
	for i, seg := range segs {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			return Ref{}, fmt.Errorf("invalid secret reference %q: empty path segment", raw)
		}
		// Secret references produced by tools like `op item get --format json`
		// percent-encode spaces and other special characters.
		if strings.Contains(seg, "%") {
			if dec, err := url.PathUnescape(seg); err == nil {
				seg = dec
			}
		}
		segs[i] = seg
	}

	ref := Ref{Attribute: attribute}
	switch len(segs) {
	case 2:
		ref.Vault, ref.Item = segs[0], segs[1]
	case 3:
		ref.Vault, ref.Item, ref.Field = segs[0], segs[1], segs[2]
	case 4:
		ref.Vault, ref.Item, ref.Section, ref.Field = segs[0], segs[1], segs[2], segs[3]
	default:
		return Ref{}, fmt.Errorf("invalid secret reference %q: want op://<vault>/<item>[/<section>]/<field>", raw)
	}

	if ref.Attribute != "" && ref.WholeItem() {
		return Ref{}, fmt.Errorf("invalid secret reference %q: ?attribute=otp requires a field segment", raw)
	}
	return ref, nil
}

// Named is a secret reference with the interpolation name assigned to it in
// a multi-reference path.
type Named struct {
	Name string // empty for a bare single reference
	Ref  Ref
}

// namedEntry matches "name = op://..." lines. Names are restricted to
// interpolation-safe characters so ${secret.<block>.<name>} always works.
var namedEntry = regexp.MustCompile(`^([A-Za-z0-9_]+)\s*=\s*(\S.*)$`)

// ParseAll parses a secret block path that is either a single reference
// ("op://vault/item/field") or a list of named references, one per line (or
// comma-separated):
//
//	db_password = op://Production/database/password
//	api_key     = op://Production/api/credential
//
// A bare single reference is returned as one Named entry with an empty
// Name. Multi-mode is entered only when the path starts with "name ="; a
// single reference containing commas is never split.
func ParseAll(raw string) ([]Named, error) {
	// Comment (#) and blank lines are ignored throughout.
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("no secret references found")
	}

	// Multi-mode is decided by the first meaningful line. A bare single
	// reference is never split, so commas in vault or item names are safe.
	if !namedEntry.MatchString(lines[0]) {
		if len(lines) > 1 {
			return nil, fmt.Errorf("multiple references must be named: want one name = op://... entry per line")
		}
		ref, err := Parse(lines[0])
		if err != nil {
			return nil, err
		}
		return []Named{{Ref: ref}}, nil
	}

	var entries []Named
	seen := map[string]bool{}
	for _, line := range lines {
		// Within named mode, a line may hold several comma-separated
		// entries; names containing commas are impossible, but item
		// names with commas need one entry per line instead.
		for _, part := range strings.Split(line, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			m := namedEntry.FindStringSubmatch(part)
			if m == nil {
				return nil, fmt.Errorf("invalid entry %q: want name = op://<vault>/<item>[/<section>]/<field> (names may contain only letters, digits and _)", part)
			}
			name := m[1]
			if seen[name] {
				return nil, fmt.Errorf("duplicate secret name %q", name)
			}
			seen[name] = true
			ref, err := Parse(m[2])
			if err != nil {
				return nil, fmt.Errorf("entry %q: %w", name, err)
			}
			entries = append(entries, Named{Name: name, Ref: ref})
		}
	}
	return entries, nil
}

// splitQuery splits "path?attribute=x" into path and attribute value.
func splitQuery(s string) (path, attribute string) {
	path, query, found := strings.Cut(s, "?")
	if !found {
		return path, ""
	}
	vals, err := url.ParseQuery(query)
	if err != nil {
		return path, query // let Parse reject it as unsupported
	}
	return path, vals.Get("attribute")
}
