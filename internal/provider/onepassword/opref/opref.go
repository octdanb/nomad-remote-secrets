// Package opref parses 1Password secret references of the form
// op://<vault>/<item>[/<section>]/<field>. Vault and item segments accept
// either a name/title or a 1Password ID. The section and field segments are
// optional: a two-segment reference (op://vault/item) addresses the whole
// item, in which case every non-empty field is returned as a key/value pair.
package opref

import (
	"fmt"
	"net/url"
	"strings"
)

// Ref is a parsed 1Password secret reference.
type Ref struct {
	Vault   string // vault name or ID (required)
	Item    string // item title or ID (required)
	Section string // section label or ID (optional, only with Field)
	Field   string // field label or ID (optional)

	// Attribute is the optional ?attribute= query parameter. "file" forces
	// fetching the file content of a document item or file field.
	Attribute string

	// Encoding is the optional ?encoding= query parameter. "base64" forces
	// base64-only output for a file reference, skipping the utf8 "value" key.
	Encoding string
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
	q := url.Values{}
	if r.Attribute != "" {
		q.Set("attribute", r.Attribute)
	}
	if r.Encoding != "" {
		q.Set("encoding", r.Encoding)
	}
	if len(q) > 0 {
		s += "?" + q.Encode()
	}
	return s
}

// IsFile reports whether the reference forces file-content semantics via
// ?attribute=file.
func (r Ref) IsFile() bool { return r.Attribute == "file" }

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

	var attribute, encoding string
	s, attribute, encoding = splitQuery(s)
	switch attribute {
	case "", "file":
	default:
		return Ref{}, fmt.Errorf("unsupported attribute %q: only \"file\" is supported", attribute)
	}
	switch encoding {
	case "", "base64":
	default:
		return Ref{}, fmt.Errorf("unsupported encoding %q: only \"base64\" is supported", encoding)
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

	ref := Ref{Attribute: attribute, Encoding: encoding}
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
	return ref, nil
}

// splitQuery splits "path?attribute=x&encoding=y" into the path and the
// attribute/encoding query values.
func splitQuery(s string) (path, attribute, encoding string) {
	path, query, found := strings.Cut(s, "?")
	if !found {
		return path, "", ""
	}
	vals, err := url.ParseQuery(query)
	if err != nil {
		return path, query, "" // let Parse reject it as unsupported
	}
	return path, vals.Get("attribute"), vals.Get("encoding")
}
