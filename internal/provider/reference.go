package provider

import (
	"fmt"
	"regexp"
	"strings"
)

// Entry is one reference from a secret block path, with the interpolation
// name assigned to it in a multi-reference path. Name is empty for a bare
// single reference. Ref is the raw, backend-neutral reference string; it is
// not parsed here — the routed Provider interprets it.
type Entry struct {
	Name string // empty for a bare single reference
	Ref  string
}

// namedEntry matches "name = <reference>" lines. Names are restricted to
// interpolation-safe characters so ${secret.<block>.<name>} always works.
var namedEntry = regexp.MustCompile(`^([A-Za-z0-9_]+)\s*=\s*(\S.*)$`)

// SplitEntries splits a secret block path into entries. The path is either a
// single reference or a list of named references, one per line (or
// comma-separated):
//
//	db_password = op://Production/database/password
//	api_key     = op://Production/api/credential
//
// A bare single reference is returned as one Entry with an empty Name.
// Multi-mode is entered only when the first meaningful line starts with
// "name ="; a single reference containing commas is never split. The
// reference is returned as an opaque string — scheme parsing is the routed
// Provider's job.
func SplitEntries(raw string) ([]Entry, error) {
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
	// reference is never split, so commas in a reference are safe.
	if !namedEntry.MatchString(lines[0]) {
		if len(lines) > 1 {
			return nil, fmt.Errorf("multiple references must be named: want one name = <reference> entry per line")
		}
		return []Entry{{Ref: lines[0]}}, nil
	}

	var entries []Entry
	seen := map[string]bool{}
	for _, line := range lines {
		// Within named mode, a line may hold several comma-separated
		// entries.
		for _, part := range strings.Split(line, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			m := namedEntry.FindStringSubmatch(part)
			if m == nil {
				return nil, fmt.Errorf("invalid entry %q: want name = <reference> (names may contain only letters, digits and _)", part)
			}
			name := m[1]
			if seen[name] {
				return nil, fmt.Errorf("duplicate secret name %q", name)
			}
			seen[name] = true
			entries = append(entries, Entry{Name: name, Ref: m[2]})
		}
	}
	return entries, nil
}
