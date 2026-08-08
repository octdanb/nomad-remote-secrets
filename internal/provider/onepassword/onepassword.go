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

	"github.com/octdanb/nomad-remote-secrets/internal/provider"
	"github.com/octdanb/nomad-remote-secrets/internal/provider/onepassword/connect"
	"github.com/octdanb/nomad-remote-secrets/internal/provider/onepassword/opitem"
	"github.com/octdanb/nomad-remote-secrets/internal/provider/onepassword/opref"
	"github.com/octdanb/nomad-remote-secrets/internal/provider/onepassword/serviceaccount"
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
	MaxFileBytes        int64         // SECRET_MAX_FILE_BYTES (0 disables)
}

// Source is the backend that resolves vaults and items. Two implementations
// exist: the Connect REST client and the service-account SDK backend.
type Source interface {
	GetVault(ctx context.Context, nameOrID string) (*opitem.Vault, error)
	GetItem(ctx context.Context, vaultID, nameOrID string) (*opitem.Item, error)
	// ListVaults returns every vault the credentials can see; used by the
	// check command to verify connectivity and token scope.
	ListVaults(ctx context.Context) ([]opitem.Vault, error)
	// GetFileContent fetches the raw bytes of a file attached to an item —
	// a document or a file-field attachment.
	GetFileContent(ctx context.Context, vaultID, itemID, fileID string) ([]byte, error)
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
	return resolve(ctx, p.src, r, p.cfg.MaxFileBytes)
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

func (l *lazyServiceAccount) GetFileContent(ctx context.Context, vaultID, itemID, fileID string) ([]byte, error) {
	if err := l.ensure(ctx); err != nil {
		return nil, err
	}
	return l.src.GetFileContent(ctx, vaultID, itemID, fileID)
}

// resolve turns a parsed reference into the Result handed to Nomad.
func resolve(ctx context.Context, src Source, ref opref.Ref, maxFileBytes int64) (provider.Result, error) {
	vault, err := src.GetVault(ctx, ref.Vault)
	if err != nil {
		return provider.Result{}, fmt.Errorf("resolving %s: %w", ref, err)
	}
	item, err := src.GetItem(ctx, vault.ID, ref.Item)
	if err != nil {
		return provider.Result{}, fmt.Errorf("resolving %s: %w", ref, err)
	}

	if ref.WholeItem() {
		// A Document item, or ?attribute=file on any item, resolves to the
		// item's file content rather than its fields.
		if ref.IsFile() || strings.EqualFold(item.Category, "DOCUMENT") {
			file, err := documentFile(item, ref)
			if err != nil {
				return provider.Result{}, err
			}
			return fileResult(ctx, src, vault.ID, item, file, ref, maxFileBytes)
		}
		return provider.Result{Values: itemValues(item), Object: true}, nil
	}

	field, err := findField(item, ref)
	if err != nil {
		return provider.Result{}, err
	}

	// A FILE-type field, or ?attribute=file on a field, resolves to the
	// attached file's content.
	if ref.IsFile() || field.FileID != "" {
		file, err := fieldFile(item, field, ref)
		if err != nil {
			return provider.Result{}, err
		}
		return fileResult(ctx, src, vault.ID, item, file, ref, maxFileBytes)
	}

	value := field.Value
	if ref.Attribute == "otp" {
		if field.TOTP == "" {
			return provider.Result{}, fmt.Errorf("%s: field is not a one-time password field", ref)
		}
		value = field.TOTP
	}

	// "value" is the stable key for single-field references; the
	// sanitized field label is included as a convenience alias.
	values := map[string]string{"value": value}
	if k := sanitizeKey(fieldKey(*field)); k != "" && k != "value" {
		values[k] = value
	}
	return provider.Result{Values: values, Object: false}, nil
}

// documentFile picks the file backing a whole-item file reference. A Document
// item has exactly one document file; ?attribute=file on a non-document item
// falls back to a sole attachment.
func documentFile(item *opitem.Item, ref opref.Ref) (opitem.File, error) {
	// Prefer a document (top-level, not tied to a field).
	var docs []opitem.File
	for _, f := range item.Files {
		if f.FieldID == "" {
			docs = append(docs, f)
		}
	}
	switch {
	case len(docs) == 1:
		return docs[0], nil
	case len(docs) == 0 && len(item.Files) == 1:
		return item.Files[0], nil
	case len(item.Files) == 0:
		return opitem.File{}, fmt.Errorf("%s: item %q has no file content", ref, item.Title)
	default:
		return opitem.File{}, fmt.Errorf("%s: item %q has multiple files; reference a field instead", ref, item.Title)
	}
}

// fieldFile picks the file backing a FILE-type field reference.
func fieldFile(item *opitem.Item, field *opitem.Field, ref opref.Ref) (opitem.File, error) {
	id := field.FileID
	for _, f := range item.Files {
		if (id != "" && f.ID == id) || (id == "" && f.FieldID == field.ID) {
			return f, nil
		}
	}
	return opitem.File{}, fmt.Errorf("%s: field %q has no attached file", ref, ref.Field)
}

// fileResult fetches a file's bytes, enforces the size guardrail, and builds
// the file Result. ?encoding=base64 drops the utf8 "value" key.
func fileResult(ctx context.Context, src Source, vaultID string, item *opitem.Item, file opitem.File, ref opref.Ref, maxFileBytes int64) (provider.Result, error) {
	if maxFileBytes > 0 && int64(file.Size) > maxFileBytes {
		return provider.Result{}, fmt.Errorf("%s: file %q is %d bytes, exceeding SECRET_MAX_FILE_BYTES=%d", ref, file.Name, file.Size, maxFileBytes)
	}
	data, err := src.GetFileContent(ctx, vaultID, item.ID, file.ID)
	if err != nil {
		return provider.Result{}, fmt.Errorf("resolving %s: %w", ref, err)
	}
	if maxFileBytes > 0 && int64(len(data)) > maxFileBytes {
		return provider.Result{}, fmt.Errorf("%s: file %q is %d bytes, exceeding SECRET_MAX_FILE_BYTES=%d", ref, file.Name, len(data), maxFileBytes)
	}

	res := provider.FileResult(file.Name, data)
	if ref.Encoding == "base64" {
		delete(res.Values, "value")
	}
	return res, nil
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
