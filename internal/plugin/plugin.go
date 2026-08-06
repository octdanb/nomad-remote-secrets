// Package plugin implements the Nomad secret provider operations
// (fingerprint and fetch) on top of 1Password Connect.
//
// Nomad runs the binary as `<plugin> fingerprint` when the client agent
// starts, and `<plugin> fetch <path>` for every secret block that names this
// provider. Both operations must print a single JSON object to stdout.
package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/octdanb/nomad-secret-plugin/internal/cache"
	"github.com/octdanb/nomad-secret-plugin/internal/connect"
	"github.com/octdanb/nomad-secret-plugin/internal/opitem"
	"github.com/octdanb/nomad-secret-plugin/internal/opref"
	"github.com/octdanb/nomad-secret-plugin/internal/serviceaccount"
)

// Source is the backend that resolves vaults and items. Two implementations
// exist: the Connect REST client and the service-account SDK backend.
type Source interface {
	GetVault(ctx context.Context, nameOrID string) (*opitem.Vault, error)
	GetItem(ctx context.Context, vaultID, nameOrID string) (*opitem.Item, error)
}

// Version is reported to Nomad in the fingerprint response and used to
// register the plugin on each client node.
const Version = "0.3.0"

// ConfigPaths are the host configuration files consulted in order; the first
// one that exists wins. Values from the host file take precedence over
// process environment variables so that a job's secret env{} block cannot
// redirect the agent-level Connect token to a host the operator didn't
// choose.
var ConfigPaths = []string{
	"/etc/nomad-secret-onepassword/config.env",
	"/etc/nomad.d/onepassword.env",
}

// Config holds everything a fetch needs. Exactly one backend is used per
// fetch: a service account token (direct to 1password.com) or a Connect
// server; the service account wins when both are configured.
type Config struct {
	ServiceAccountToken string        // OP_SERVICE_ACCOUNT_TOKEN / OP_SERVICE_ACCOUNT_TOKEN_FILE
	ConnectHost         string        // OP_CONNECT_HOST
	Token               string        // OP_CONNECT_TOKEN / OP_CONNECT_TOKEN_FILE
	Timeout             time.Duration // OP_REQUEST_TIMEOUT (default 30s)
	CacheDir            string        // OP_CACHE_DIR
	CacheTTL            time.Duration // OP_CACHE_TTL (default 5m, 0 disables)
	MaxStale            time.Duration // OP_CACHE_MAX_STALE (default 24h, 0 disables fallback)
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
	entries, err := opref.ParseAll(path)
	if err != nil {
		return nil, err
	}

	cfg, err := LoadConfig(ConfigPaths, os.Getenv)
	if err != nil {
		return nil, err
	}

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

	// One deadline covers all references so the plugin stays inside
	// Nomad's 60-second kill window regardless of entry count.
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	src := newSource(cfg)

	merged := map[string]string{}
	for _, entry := range entries {
		values, err := fetchOne(ctx, stderr, cfg, store, src, entry.Ref)
		if err != nil {
			return nil, err
		}

		switch {
		case entry.Name == "":
			// Bare single reference: keep the flat key set
			// ("value" plus field/label keys).
			return values, nil
		case entry.Ref.WholeItem():
			// Named whole item: prefix every field with the name,
			// e.g. twilio_username, twilio_password.
			for k, v := range values {
				merged[entry.Name+"_"+k] = v
			}
		default:
			merged[entry.Name] = values["value"]
		}
	}
	return merged, nil
}

// newSource picks the backend from the configuration. A service account
// token wins over Connect settings — configuring one is an explicit choice,
// while Connect variables can linger in an agent's environment.
//
// The service-account source is wrapped so SDK authentication happens on
// first use, inside fetchOne — that way an unreachable 1password.com still
// falls back to the stale cache instead of failing before the cache is
// consulted.
func newSource(cfg Config) Source {
	if cfg.ServiceAccountToken != "" {
		return &lazyServiceAccount{token: cfg.ServiceAccountToken}
	}
	return connect.New(cfg.ConnectHost, cfg.Token, cfg.Timeout)
}

type lazyServiceAccount struct {
	token string
	src   *serviceaccount.Source
}

func (l *lazyServiceAccount) ensure(ctx context.Context) error {
	if l.src != nil {
		return nil
	}
	src, err := serviceaccount.New(ctx, l.token, Version)
	if err != nil {
		return err
	}
	l.src = src
	return nil
}

func (l *lazyServiceAccount) GetVault(ctx context.Context, nameOrID string) (*opitem.Vault, error) {
	if err := l.ensure(ctx); err != nil {
		return nil, err
	}
	return l.src.GetVault(ctx, nameOrID)
}

func (l *lazyServiceAccount) GetItem(ctx context.Context, vaultID, nameOrID string) (*opitem.Item, error) {
	if err := l.ensure(ctx); err != nil {
		return nil, err
	}
	return l.src.GetItem(ctx, vaultID, nameOrID)
}

// fetchOne resolves a single reference through the cache: fresh cache hit,
// then a live fetch, then — if the backend is unreachable — a stale cache
// entry within the configured age bound.
func fetchOne(ctx context.Context, stderr io.Writer, cfg Config, store *cache.Cache, src Source, ref opref.Ref) (map[string]string, error) {
	cacheKey := cfg.cacheScope() + "|" + ref.String()

	// OTP codes rotate every 30 seconds; serving them from cache would
	// hand out expired codes.
	cacheable := ref.Attribute != "otp"

	if store != nil && cacheable {
		if values, ok := store.Get(cacheKey); ok {
			return values, nil
		}
	}

	values, err := resolve(ctx, src, ref)
	if err != nil {
		if store != nil && cacheable && cfg.MaxStale > 0 {
			if stale, age, ok := store.Stale(cacheKey, cfg.MaxStale); ok {
				fmt.Fprintf(stderr, "onepassword: 1Password unavailable (%v); serving cached value %s old for %s\n",
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
func resolve(ctx context.Context, src Source, ref opref.Ref) (map[string]string, error) {
	vault, err := src.GetVault(ctx, ref.Vault)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", ref, err)
	}
	item, err := src.GetItem(ctx, vault.ID, ref.Item)
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
func itemValues(item *opitem.Item) map[string]string {
	sections := sectionLabels(item)
	values := map[string]string{}

	for _, f := range item.Fields {
		if f.Value == "" {
			continue
		}
		key := fieldKey(f)
		if f.SectionID != "" {
			if label := sections[f.SectionID]; label != "" {
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
		if f.Value == "" || f.SectionID != "" {
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
func findField(item *opitem.Item, ref opref.Ref) (*opitem.Field, error) {
	sections := sectionLabels(item)

	var matches []*opitem.Field
	for i := range item.Fields {
		f := &item.Fields[i]

		if ref.Section != "" {
			if f.SectionID == "" {
				continue
			}
			label := sections[f.SectionID]
			if f.SectionID != ref.Section && !strings.EqualFold(label, ref.Section) {
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
				if f.SectionID == "" {
					return f, nil
				}
			}
		}
		return nil, fmt.Errorf("%s: field %q is ambiguous in item %q; qualify it with a section", ref, ref.Field, item.Title)
	}
}

func sectionLabels(item *opitem.Item) map[string]string {
	labels := make(map[string]string, len(item.Sections))
	for _, s := range item.Sections {
		labels[s.ID] = s.Label
	}
	return labels
}

func fieldKey(f opitem.Field) string {
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
