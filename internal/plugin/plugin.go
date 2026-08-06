// Package plugin implements the Nomad secret provider operations
// (fingerprint and fetch) on top of 1Password Connect.
//
// Nomad runs the binary as `<plugin> fingerprint` when the client agent
// starts, and `<plugin> fetch <path>` for every secret block that names this
// provider. Both operations must print a single JSON object to stdout.
package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/octdanb/nomad-secret-plugin/internal/cache"
	"github.com/octdanb/nomad-secret-plugin/internal/connect"
	"github.com/octdanb/nomad-secret-plugin/internal/opref"
)

// Version is reported to Nomad in the fingerprint response and used to
// register the plugin on each client node.
const Version = "0.1.0"

// ConfigPaths are the host configuration files consulted in order; the first
// one that exists wins. Values from the host file take precedence over
// process environment variables so that a job's secret env{} block cannot
// redirect the agent-level Connect token to a host the operator didn't
// choose.
var ConfigPaths = []string{
	"/etc/nomad-secret-onepassword/config.env",
	"/etc/nomad.d/onepassword.env",
}

// Config holds everything a fetch needs.
type Config struct {
	ConnectHost string        // OP_CONNECT_HOST
	Token       string        // OP_CONNECT_TOKEN / OP_CONNECT_TOKEN_FILE
	Timeout     time.Duration // OP_REQUEST_TIMEOUT (default 30s)
	CacheDir    string        // OP_CACHE_DIR
	CacheTTL    time.Duration // OP_CACHE_TTL (default 5m, 0 disables)
	MaxStale    time.Duration // OP_CACHE_MAX_STALE (default 24h, 0 disables fallback)
}

// fetchResponse is the JSON shape Nomad expects on stdout for fetch.
type fetchResponse struct {
	Result map[string]string `json:"result"`
	Error  string            `json:"error,omitempty"`
}

// Fingerprint prints the registration response Nomad expects on agent
// startup or SIGHUP.
func Fingerprint(stdout io.Writer) error {
	return json.NewEncoder(stdout).Encode(map[string]string{
		"type":    "secrets",
		"version": Version,
	})
}

// Fetch resolves the secret reference in path and prints the fetch response.
// Errors are reported inside the JSON response (as the spec allows) rather
// than via exit codes, so Nomad can surface the message to the operator.
func Fetch(stdout, stderr io.Writer, path string) {
	values, err := fetch(stderr, path)
	if err != nil {
		json.NewEncoder(stdout).Encode(fetchResponse{Result: map[string]string{}, Error: err.Error()})
		return
	}
	json.NewEncoder(stdout).Encode(fetchResponse{Result: values})
}

func fetch(stderr io.Writer, path string) (map[string]string, error) {
	ref, err := opref.Parse(path)
	if err != nil {
		return nil, err
	}

	cfg, err := LoadConfig(ConfigPaths, os.Getenv)
	if err != nil {
		return nil, err
	}

	// The cache key includes the Connect host and a digest of the token so
	// entries are never shared across servers or across tokens with
	// different vault access.
	tokenSum := sha256.Sum256([]byte(cfg.Token))
	cacheKey := cfg.ConnectHost + "|" + hex.EncodeToString(tokenSum[:8]) + "|" + ref.String()

	var store *cache.Cache
	if cfg.CacheDir != "" {
		store, err = cache.New(cfg.CacheDir, cfg.CacheTTL)
		if err != nil {
			// A broken cache should degrade to uncached fetches, not
			// block deploys.
			fmt.Fprintf(stderr, "onepassword: cache disabled: %v\n", err)
			store = nil
		}
	}

	// OTP codes rotate every 30 seconds; serving them from cache would
	// hand out expired codes.
	cacheable := ref.Attribute != "otp"

	if store != nil && cacheable {
		if values, ok := store.Get(cacheKey); ok {
			return values, nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	values, err := resolve(ctx, connect.New(cfg.ConnectHost, cfg.Token, cfg.Timeout), ref)
	if err != nil {
		if store != nil && cacheable && cfg.MaxStale > 0 {
			if stale, age, ok := store.Stale(cacheKey, cfg.MaxStale); ok {
				fmt.Fprintf(stderr, "onepassword: 1Password Connect unavailable (%v); serving cached value %s old for %s\n",
					err, age.Round(time.Second), ref)
				return stale, nil
			}
		}
		return nil, err
	}

	if store != nil && cacheable {
		if err := store.Put(cacheKey, values); err != nil {
			fmt.Fprintf(stderr, "onepassword: failed to write cache: %v\n", err)
		}
	}
	return values, nil
}

// resolve turns a parsed reference into the key/value map handed to Nomad.
func resolve(ctx context.Context, client *connect.Client, ref opref.Ref) (map[string]string, error) {
	vault, err := client.GetVault(ctx, ref.Vault)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", ref, err)
	}
	item, err := client.GetItem(ctx, vault.ID, ref.Item)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", ref, err)
	}

	if ref.WholeItem() {
		return itemValues(item), nil
	}

	field, err := findField(item, ref)
	if err != nil {
		return nil, err
	}

	value := field.Value
	if ref.Attribute == "otp" {
		if field.TOTP == "" {
			return nil, fmt.Errorf("%s: field is not a one-time password field", ref)
		}
		value = field.TOTP
	}

	// "value" is the stable key for single-field references; the
	// sanitized field label is included as a convenience alias.
	values := map[string]string{"value": value}
	if k := sanitizeKey(fieldKey(*field)); k != "" && k != "value" {
		values[k] = value
	}
	return values, nil
}

// itemValues flattens every non-empty field of an item into interpolation
// keys. Fields inside a section are prefixed with the section label so that
// identically named fields in different sections don't collide.
func itemValues(item *connect.Item) map[string]string {
	sections := sectionLabels(item)
	values := map[string]string{}

	for _, f := range item.Fields {
		if f.Value == "" {
			continue
		}
		key := fieldKey(f)
		if f.Section != nil {
			if label := sections[f.Section.ID]; label != "" {
				key = label + "_" + key
			}
		}
		key = sanitizeKey(key)
		if key == "" {
			continue
		}
		// First field wins on collision; item field order is stable.
		if _, exists := values[key]; !exists {
			values[key] = f.Value
		}
	}

	// Guarantee the conventional keys for login items even when a label
	// was customized.
	for _, f := range item.Fields {
		if f.Value == "" || f.Section != nil {
			continue
		}
		switch f.Purpose {
		case "USERNAME":
			setDefault(values, "username", f.Value)
		case "PASSWORD":
			setDefault(values, "password", f.Value)
		case "NOTES":
			setDefault(values, "notes", f.Value)
		}
	}
	return values
}

// findField locates the field a reference addresses, honouring the optional
// section qualifier. Labels are matched case-insensitively; IDs exactly.
func findField(item *connect.Item, ref opref.Ref) (*connect.Field, error) {
	sections := sectionLabels(item)

	var matches []*connect.Field
	for i := range item.Fields {
		f := &item.Fields[i]

		if ref.Section != "" {
			if f.Section == nil {
				continue
			}
			label := sections[f.Section.ID]
			if f.Section.ID != ref.Section && !strings.EqualFold(label, ref.Section) {
				continue
			}
		}

		if f.ID == ref.Field || strings.EqualFold(f.Label, ref.Field) ||
			strings.EqualFold(f.Purpose, ref.Field) {
			matches = append(matches, f)
		}
	}

	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("%s: no field %q in item %q", ref, ref.Field, item.Title)
	case 1:
		return matches[0], nil
	default:
		// Prefer a field outside any section when the reference has no
		// section qualifier — mirrors how 1Password resolves ambiguity.
		if ref.Section == "" {
			for _, f := range matches {
				if f.Section == nil {
					return f, nil
				}
			}
		}
		return nil, fmt.Errorf("%s: field %q is ambiguous in item %q; qualify it with a section", ref, ref.Field, item.Title)
	}
}

func sectionLabels(item *connect.Item) map[string]string {
	labels := make(map[string]string, len(item.Sections))
	for _, s := range item.Sections {
		labels[s.ID] = s.Label
	}
	return labels
}

func fieldKey(f connect.Field) string {
	if f.Label != "" {
		return f.Label
	}
	return f.ID
}

var keyCleaner = regexp.MustCompile(`[^A-Za-z0-9_]+`)

// sanitizeKey rewrites a field label into a key that is safe to use in
// Nomad's ${secret.<name>.<key>} interpolation syntax.
func sanitizeKey(s string) string {
	return strings.Trim(keyCleaner.ReplaceAllString(s, "_"), "_")
}

func setDefault(m map[string]string, key, value string) {
	if _, ok := m[key]; !ok {
		m[key] = value
	}
}
