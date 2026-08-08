// Package provider defines the backend-neutral abstraction the Nomad secret
// plugin resolves references through. A Provider knows how to interpret and
// resolve references for one backend (e.g. 1Password); a Registry routes a
// raw reference string to the right Provider by its scheme.
package provider

import (
	"context"
	"fmt"
	"strings"
)

// Result is what a Provider returns for a resolved reference.
type Result struct {
	// Values are the interpolation keys exposed to Nomad. Scalars always
	// carry at least "value".
	Values map[string]string
	// Object is true for a whole-item / JSON-object reference; it drives
	// named-entry key prefixing in the plugin's fetch loop.
	Object bool
}

// Provider resolves references for a single backend.
type Provider interface {
	// CacheKey returns a stable, backend-namespaced cache key for ref and
	// whether the ref may be cached (false, e.g., for 1Password OTP). It is
	// called before Resolve so uncacheable refs skip the cache entirely.
	CacheKey(ref string) (key string, cacheable bool, err error)
	// Resolve turns a reference into its interpolation key/value map.
	Resolve(ctx context.Context, ref string) (Result, error)
	// Ping checks connectivity and credential scope for the check command.
	Ping(ctx context.Context) error
	// Describe names the active backend for diagnostics; never secrets.
	Describe() string
}

// Registry routes reference strings to providers by scheme.
type Registry struct {
	byScheme map[string]Provider
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byScheme: map[string]Provider{}}
}

// Register associates a scheme with a provider.
func (r *Registry) Register(scheme string, p Provider) {
	r.byScheme[scheme] = p
}

// Route returns the provider registered for ref's scheme. A reference with no
// scheme is routed to the sole registered provider when exactly one is
// configured — preserving back-compat for scheme-less op:// paths — but is
// rejected as ambiguous once more than one backend is in play.
func (r *Registry) Route(ref string) (Provider, error) {
	scheme := Scheme(ref)
	if scheme == "" {
		if len(r.byScheme) == 1 {
			for _, p := range r.byScheme {
				return p, nil
			}
		}
		return nil, fmt.Errorf("secret reference %q has no scheme; qualify it (e.g. op://…) when more than one backend is configured", ref)
	}
	p, ok := r.byScheme[scheme]
	if !ok {
		return nil, fmt.Errorf("unknown secret reference scheme %q", scheme)
	}
	return p, nil
}

// Scheme extracts the backend scheme from a reference. It recognises both
// "scheme://..." (e.g. op://) and "scheme:..." (e.g. aws-ssm:) forms,
// returning the bare scheme token ("op", "aws-ssm"). It returns "" when no
// scheme prefix is present.
func Scheme(ref string) string {
	ref = strings.TrimSpace(ref)
	if i := strings.Index(ref, "://"); i > 0 {
		return ref[:i]
	}
	if i := strings.Index(ref, ":"); i > 0 {
		return ref[:i]
	}
	return ""
}
