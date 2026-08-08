// Package plugin implements the Nomad secret provider operations
// (fingerprint and fetch) on top of a provider-neutral backend abstraction.
//
// Nomad runs the binary as `<plugin> fingerprint` when the client agent
// starts, and `<plugin> fetch <path>` for every secret block that names this
// provider. Both operations must print a single JSON object to stdout. The
// reference scheme selects the backend at fetch time (only op:// today).
package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/octdanb/nomad-secret-plugin/internal/cache"
	"github.com/octdanb/nomad-secret-plugin/internal/provider"
	"github.com/octdanb/nomad-secret-plugin/internal/provider/awsssm"
	"github.com/octdanb/nomad-secret-plugin/internal/provider/onepassword"
)

// Version is reported to Nomad in the fingerprint response and used to
// register the plugin on each client node.
const Version = "0.4.0"

// ConfigPaths are the host configuration files consulted in order; the first
// one that exists wins. Values from the host file take precedence over
// process environment variables so that a job's secret env{} block cannot
// redirect the agent-level Connect token to a host the operator didn't
// choose.
var ConfigPaths = []string{
	"/etc/nomad-secret-onepassword/config.env",
	"/etc/nomad.d/onepassword.env",
}

// newRegistry builds the provider registry from the loaded configuration.
// Each provider is registered only when its backend is configured, so a
// single-backend cluster keeps the scheme-less fallback (Route resolves a
// bare reference to the sole provider) and a dual-backend cluster correctly
// requires every reference to name its scheme.
func newRegistry(cfg Config) *provider.Registry {
	reg := provider.NewRegistry()
	if cfg.OPEnabled() {
		reg.Register(onepassword.Scheme, onepassword.New(onepassword.Config{
			ServiceAccountToken: cfg.ServiceAccountToken,
			ConnectHost:         cfg.ConnectHost,
			Token:               cfg.Token,
			Timeout:             cfg.Timeout,
			Version:             Version,
		}))
	}
	if cfg.AWSEnabled() {
		reg.Register(awsssm.Scheme, awsssm.New(awsssm.Config{
			Region:      cfg.AWSRegion,
			Profile:     cfg.AWSProfile,
			EndpointURL: cfg.AWSEndpointURL,
			Decrypt:     cfg.AWSDecrypt,
			Timeout:     cfg.Timeout,
		}))
	}
	return reg
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
	entries, err := provider.SplitEntries(path)
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
	reg := newRegistry(cfg)

	merged := map[string]string{}
	for _, entry := range entries {
		p, err := reg.Route(entry.Ref)
		if err != nil {
			if entry.Name != "" {
				err = fmt.Errorf("entry %q: %w", entry.Name, err)
			}
			return nil, err
		}

		result, err := fetchOne(ctx, stderr, cfg, store, p, entry.Ref)
		if err != nil {
			if entry.Name != "" {
				err = fmt.Errorf("entry %q: %w", entry.Name, err)
			}
			// This message becomes the Nomad task event the operator
			// sees, so make it name the backend and config source.
			return nil, fmt.Errorf("%w [backend: %s; config: %s; try `onepassword check` on this node]", err, p.Describe(), cfg.Source)
		}

		switch {
		case entry.Name == "":
			// Bare single reference: keep the flat key set
			// ("value" plus field/label keys).
			return result.Values, nil
		case result.Object:
			// Named whole item / object: prefix every key with the
			// name, e.g. twilio_username, twilio_password.
			for k, v := range result.Values {
				merged[entry.Name+"_"+k] = v
			}
		default:
			merged[entry.Name] = result.Values["value"]
		}
	}
	return merged, nil
}

// fetchOne resolves a single reference through the cache: fresh cache hit,
// then a live resolve, then — if the backend is unreachable — a stale cache
// entry within the configured age bound.
func fetchOne(ctx context.Context, stderr io.Writer, cfg Config, store *cache.Cache, p provider.Provider, ref string) (provider.Result, error) {
	cacheKey, cacheable, err := p.CacheKey(ref)
	if err != nil {
		return provider.Result{}, err
	}

	if store != nil && cacheable {
		if values, ok := store.Get(cacheKey); ok {
			return provider.Result{Values: values, Object: isObject(values)}, nil
		}
	}

	result, err := p.Resolve(ctx, ref)
	if err != nil {
		if store != nil && cacheable && cfg.MaxStale > 0 {
			if stale, age, ok := store.Stale(cacheKey, cfg.MaxStale); ok {
				fmt.Fprintf(stderr, "onepassword: backend unavailable (%v); serving cached value %s old for %s\n",
					err, age.Round(time.Second), ref)
				return provider.Result{Values: stale, Object: isObject(stale)}, nil
			}
		}
		return provider.Result{}, err
	}

	if store != nil && cacheable {
		if err := store.Put(cacheKey, result.Values); err != nil {
			fmt.Fprintf(stderr, "onepassword: failed to write cache: %v\n", err)
		}
	}
	return result, nil
}

// isObject reports whether a cached value set represents a whole item /
// object rather than a scalar. A scalar always carries "value"; a whole item
// never does, so its named-entry keys get the name_ prefix. The Object flag
// isn't persisted in the cache, so it is reconstructed on a cache hit.
func isObject(values map[string]string) bool {
	_, hasValue := values["value"]
	return !hasValue
}
