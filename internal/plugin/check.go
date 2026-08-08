package plugin

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/octdanb/nomad-secret-plugin/internal/cache"
	"github.com/octdanb/nomad-secret-plugin/internal/provider"
	"github.com/octdanb/nomad-secret-plugin/internal/provider/awssm"
	"github.com/octdanb/nomad-secret-plugin/internal/provider/awsssm"
	"github.com/octdanb/nomad-secret-plugin/internal/provider/onepassword"
)

// Check is the operator diagnostic behind `secrets check [reference]`.
// It reports how the plugin is configured on this node, verifies
// connectivity and credential scope, and optionally dry-runs a secret
// reference (or a multi-entry path). Secret values are never printed — for
// references it prints only the interpolation keys that would be exposed.
// It returns the process exit code.
func Check(w io.Writer, path string) int {
	fmt.Fprintf(w, "secrets provider v%s — diagnostic\n\n", Version)

	cfg, err := LoadConfig(ConfigPaths, os.Getenv)
	if err != nil {
		fmt.Fprintf(w, "FAIL config: %v\n", err)
		fmt.Fprintf(w, "     config files checked: %s\n", strings.Join(ConfigPaths, ", "))
		return 1
	}
	fmt.Fprintf(w, "OK   config loaded from: %s\n", cfg.Source)
	fmt.Fprintf(w, "OK   backend: %s\n", cfg.Describe())
	fmt.Fprintf(w, "     request timeout %s, cache TTL %s, max stale %s\n", cfg.Timeout, cfg.CacheTTL, cfg.MaxStale)

	if cfg.CacheDir == "" {
		fmt.Fprintf(w, "OK   cache: disabled\n")
	} else if _, err := cache.New(cfg.CacheDir, cfg.CacheTTL); err != nil {
		fmt.Fprintf(w, "WARN cache: %v (fetches will work but nothing is cached)\n", err)
	} else {
		fmt.Fprintf(w, "OK   cache: %s\n", cfg.CacheDir)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	// The connectivity summary is backend-specific. The op:// provider can
	// enumerate the visible vaults, which is the most useful scope check; AWS
	// verifies creds/region/connectivity with a cheap DescribeParameters.
	if cfg.OPEnabled() {
		op := onepassword.New(onepassword.Config{
			ServiceAccountToken: cfg.ServiceAccountToken,
			ConnectHost:         cfg.ConnectHost,
			Token:               cfg.Token,
			Timeout:             cfg.Timeout,
			Version:             Version,
		})
		vaults, err := op.ListVaults(ctx)
		if err != nil {
			fmt.Fprintf(w, "FAIL connectivity: %v\n", err)
			fmt.Fprintf(w, "     hint: %s\n", hintFor(err))
			return 1
		}
		names := make([]string, 0, len(vaults))
		for _, v := range vaults {
			names = append(names, v.Name)
		}
		sort.Strings(names)
		fmt.Fprintf(w, "OK   connectivity: %d vault(s) visible: %s\n", len(vaults), strings.Join(names, ", "))
		if len(vaults) == 0 {
			fmt.Fprintf(w, "WARN no vaults are visible — the credentials are valid but not granted any vault\n")
		}
	}

	if cfg.AWSEnabled() {
		aws := awsssm.New(awsssm.Config{
			Region:      cfg.AWSRegion,
			Profile:     cfg.AWSProfile,
			EndpointURL: cfg.AWSEndpointURL,
			Decrypt:     cfg.AWSDecrypt,
			Timeout:     cfg.Timeout,
		})
		if err := aws.Ping(ctx); err != nil {
			fmt.Fprintf(w, "FAIL AWS connectivity: %v\n", err)
			fmt.Fprintf(w, "     hint: %s\n", hintFor(err))
			return 1
		}
		fmt.Fprintf(w, "OK   AWS connectivity: %s reachable\n", aws.Describe())

		sm := awssm.New(awssm.Config{
			Region:      cfg.AWSRegion,
			Profile:     cfg.AWSProfile,
			EndpointURL: cfg.AWSEndpointURL,
			Timeout:     cfg.Timeout,
		})
		if err := sm.Ping(ctx); err != nil {
			fmt.Fprintf(w, "FAIL AWS connectivity: %v\n", err)
			fmt.Fprintf(w, "     hint: %s\n", hintFor(err))
			return 1
		}
		fmt.Fprintf(w, "OK   AWS connectivity: %s reachable\n", sm.Describe())
	}

	if path == "" {
		return 0
	}

	fmt.Fprintf(w, "\nResolving %q (values are never printed):\n", path)
	entries, err := provider.SplitEntries(path)
	if err != nil {
		fmt.Fprintf(w, "FAIL parse: %v\n", err)
		return 1
	}

	reg := newRegistry(cfg)
	failed := false
	for _, entry := range entries {
		label := entry.Ref
		if entry.Name != "" {
			label = entry.Name + " = " + label
		}
		p, err := reg.Route(entry.Ref)
		if err != nil {
			failed = true
			fmt.Fprintf(w, "FAIL %s\n     %v\n", label, err)
			continue
		}
		// Resolve live, bypassing the cache, so the check reflects the
		// backend's current state.
		result, err := p.Resolve(ctx, entry.Ref)
		if err != nil {
			failed = true
			fmt.Fprintf(w, "FAIL %s\n     %v\n     hint: %s\n", label, err, hintFor(err))
			continue
		}
		keys := make([]string, 0, len(result.Values))
		for k := range result.Values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(w, "OK   %s → keys: %s\n", label, strings.Join(keys, ", "))
	}
	if failed {
		return 1
	}
	return 0
}

// hintFor maps common failure modes to a next debugging step.
func hintFor(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "invalid bearer token"), strings.Contains(msg, "401"),
		strings.Contains(msg, "invalid service account token"):
		return "the token is invalid, expired, or revoked — check the token configured on this node"
	case strings.Contains(msg, "no vault named"):
		return "the vault doesn't exist, or the token isn't scoped to it — grant the vault to this credential in 1Password"
	case strings.Contains(msg, "no item titled"):
		return "check the item's title in that vault (titles match exactly), or reference the item by ID"
	case strings.Contains(msg, "no field"):
		return "check the field's label in 1Password, or qualify it with its section: op://vault/item/section/field"
	case strings.Contains(msg, "ambiguous"), strings.Contains(msg, "reference the"):
		return "more than one object matches this name — use the ID shown in 1Password instead"
	case strings.Contains(msg, "context deadline"), strings.Contains(msg, "timeout"),
		strings.Contains(msg, "connection refused"), strings.Contains(msg, "no such host"):
		return "network problem — verify this node can reach the backend (and any proxy/firewall in between)"
	default:
		return "run again with a single reference to narrow it down"
	}
}
