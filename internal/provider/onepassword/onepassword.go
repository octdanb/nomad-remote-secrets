// Package onepassword implements the op:// secret provider. It resolves
// 1Password references through either a Connect server or a service account,
// and exposes the backend behind the provider.Provider interface so the
// plugin's fetch/check loop stays backend-neutral.
package onepassword

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/octdanb/nomad-secret-plugin/internal/provider"
	"github.com/octdanb/nomad-secret-plugin/internal/provider/onepassword/connect"
	"github.com/octdanb/nomad-secret-plugin/internal/provider/onepassword/opitem"
	"github.com/octdanb/nomad-secret-plugin/internal/provider/onepassword/opref"
	"github.com/octdanb/nomad-secret-plugin/internal/provider/onepassword/serviceaccount"
)

// Scheme is the reference scheme this provider handles.
const Scheme = "op"

// Config holds the 1Password backend settings. Exactly one backend is used:
// a service account token (direct to 1password.com) or a Connect server; the
// service account wins when both are configured.
type Config struct {
	ServiceAccountToken string        // OP_SERVICE_ACCOUNT_TOKEN
	ConnectHost         string        // OP_CONNECT_HOST
	Token               string        // OP_CONNECT_TOKEN
	Timeout             time.Duration // OP_REQUEST_TIMEOUT
	Version             string        // plugin version, for SDK integration id
}

// Source is the backend that resolves vaults and items. Two implementations
// exist: the Connect REST client and the service-account SDK backend.
type Source interface {
	GetVault(ctx context.Context, nameOrID string) (*opitem.Vault, error)
	GetItem(ctx context.Context, vaultID, nameOrID string) (*opitem.Item, error)
	// ListVaults returns every vault the credentials can see; used by the
	// check command to verify connectivity and token scope.
	ListVaults(ctx context.Context) ([]opitem.Vault, error)
}

// Provider is the op:// implementation of provider.Provider.
type Provider struct {
	cfg Config
	src Source
}

// New constructs an op:// provider from the 1Password configuration.
func New(cfg Config) *Provider {
	return &Provider{cfg: cfg, src: newSource(cfg)}
}

// Describe names the active backend for error messages and diagnostics.
// Tokens are never included.
func (p *Provider) Describe() string {
	if p.cfg.ServiceAccountToken != "" {
		return "1Password service account"
	}
	return "1Password Connect at " + p.cfg.ConnectHost
}

// CacheKey returns the backend-namespaced cache key for ref and whether it
// may be cached. OTP references rotate every 30 seconds and are never cached.
func (p *Provider) CacheKey(ref string) (string, bool, error) {
	r, err := opref.Parse(ref)
	if err != nil {
		return "", false, err
	}
	cacheable := r.Attribute != "otp"
	return p.cacheScope() + "|" + r.String(), cacheable, nil
}

// Resolve parses ref and returns its interpolation key/value map.
func (p *Provider) Resolve(ctx context.Context, ref string) (provider.Result, error) {
	r, err := opref.Parse(ref)
	if err != nil {
		return provider.Result{}, err
	}
	values, err := resolve(ctx, p.src, r)
	if err != nil {
		return provider.Result{}, err
	}
	return provider.Result{Values: values, Object: r.WholeItem()}, nil
}

// Ping verifies connectivity and credential scope for the check command.
func (p *Provider) Ping(ctx context.Context) error {
	_, err := p.src.ListVaults(ctx)
	return err
}

// ListVaults exposes the visible vaults for the check command's summary.
func (p *Provider) ListVaults(ctx context.Context) ([]opitem.Vault, error) {
	return p.src.ListVaults(ctx)
}

// cacheScope returns the backend-identifying prefix for cache keys, so
// entries are never shared across servers, accounts, or tokens with
// different vault access.
func (p *Provider) cacheScope() string {
	host, token := p.cfg.ConnectHost, p.cfg.Token
	if p.cfg.ServiceAccountToken != "" {
		host, token = "service-account", p.cfg.ServiceAccountToken
	}
	sum := sha256.Sum256([]byte(token))
	return host + "|" + hex.EncodeToString(sum[:8])
}

// newSource picks the backend from the configuration. A service account
// token wins over Connect settings — configuring one is an explicit choice,
// while Connect variables can linger in an agent's environment.
//
// The service-account source is wrapped so SDK authentication happens on
// first use — that way an unreachable 1password.com still falls back to the
// stale cache instead of failing before the cache is consulted.
func newSource(cfg Config) Source {
	if cfg.ServiceAccountToken != "" {
		return &lazyServiceAccount{token: cfg.ServiceAccountToken, version: cfg.Version}
	}
	return connect.New(cfg.ConnectHost, cfg.Token, cfg.Timeout)
}

type lazyServiceAccount struct {
	token   string
	version string
	src     *serviceaccount.Source
}

func (l *lazyServiceAccount) ensure(ctx context.Context) error {
	if l.src != nil {
		return nil
	}
	src, err := serviceaccount.New(ctx, l.token, l.version)
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

func (l *lazyServiceAccount) ListVaults(ctx context.Context) ([]opitem.Vault, error) {
	if err := l.ensure(ctx); err != nil {
		return nil, err
	}
	return l.src.ListVaults(ctx)
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
