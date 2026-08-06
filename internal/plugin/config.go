package plugin

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultCacheDir is where fetched secrets are cached between plugin
// invocations unless OP_CACHE_DIR overrides it.
const DefaultCacheDir = "/var/cache/nomad-secret-onepassword"

// LoadConfig builds the fetch configuration from an optional host config
// file and the process environment. The first path in paths that exists is
// loaded; values defined there take precedence over environment variables.
//
// That precedence is deliberate: Nomad passes a job's secret env{} block
// into the plugin's process environment, so if the environment won, any job
// author could point OP_CONNECT_HOST at a server they control and have the
// operator's Connect token sent to it. With a host file in place, jobs can
// only supply settings the operator left unset.
func LoadConfig(paths []string, getenv func(string) string) (Config, error) {
	fileVals := map[string]string{}
	source := "agent environment"
	for _, p := range paths {
		vals, err := parseEnvFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return Config{}, fmt.Errorf("reading config file %s: %w", p, err)
		}
		fileVals = vals
		source = p
		break
	}

	lookup := func(key string) string {
		if v, ok := fileVals[key]; ok {
			return v
		}
		return getenv(key)
	}

	cfg := Config{
		ConnectHost: strings.TrimRight(lookup("OP_CONNECT_HOST"), "/"),
		CacheDir:    DefaultCacheDir,
		Timeout:     30 * time.Second,
		CacheTTL:    5 * time.Minute,
		MaxStale:    24 * time.Hour,
		Source:      source,
	}

	var err error
	if cfg.ServiceAccountToken, err = tokenSetting(lookup, "OP_SERVICE_ACCOUNT_TOKEN"); err != nil {
		return Config{}, err
	}
	if cfg.Token, err = tokenSetting(lookup, "OP_CONNECT_TOKEN"); err != nil {
		return Config{}, err
	}

	// Backend selection: a service account token alone is a complete
	// configuration; otherwise Connect needs both a host and a token.
	if cfg.ServiceAccountToken == "" {
		if cfg.ConnectHost == "" {
			return Config{}, fmt.Errorf("no 1Password backend configured: set OP_SERVICE_ACCOUNT_TOKEN (service account) or OP_CONNECT_HOST and OP_CONNECT_TOKEN (Connect server) in %s or in the Nomad agent environment", ConfigPaths[0])
		}
		if cfg.Token == "" {
			return Config{}, fmt.Errorf("no Connect token: set OP_CONNECT_TOKEN or OP_CONNECT_TOKEN_FILE in %s or in the Nomad agent environment", ConfigPaths[0])
		}
	}

	if cfg.Timeout, err = durationSetting(lookup, "OP_REQUEST_TIMEOUT", cfg.Timeout); err != nil {
		return Config{}, err
	}
	if cfg.CacheTTL, err = durationSetting(lookup, "OP_CACHE_TTL", cfg.CacheTTL); err != nil {
		return Config{}, err
	}
	if cfg.MaxStale, err = durationSetting(lookup, "OP_CACHE_MAX_STALE", cfg.MaxStale); err != nil {
		return Config{}, err
	}
	if v := lookup("OP_CACHE_DIR"); v != "" {
		cfg.CacheDir = v
	}
	// OP_CACHE_TTL=0 with OP_CACHE_MAX_STALE=0 means fully uncached;
	// skip the cache dir entirely so nothing is written to disk.
	if cfg.CacheTTL <= 0 && cfg.MaxStale <= 0 {
		cfg.CacheDir = ""
	}
	return cfg, nil
}

// cacheScope returns the backend-identifying prefix for cache keys, so
// entries are never shared across servers, accounts, or tokens with
// different vault access.
func (c Config) cacheScope() string {
	host, token := c.ConnectHost, c.Token
	if c.ServiceAccountToken != "" {
		host, token = "service-account", c.ServiceAccountToken
	}
	sum := sha256.Sum256([]byte(token))
	return host + "|" + hex.EncodeToString(sum[:8])
}

// tokenSetting reads a token from <key> or, failing that, from the file
// named by <key>_FILE.
func tokenSetting(lookup func(string) string, key string) (string, error) {
	if v := lookup(key); v != "" {
		return v, nil
	}
	if file := lookup(key + "_FILE"); file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading %s_FILE: %w", key, err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	return "", nil
}

// durationSetting parses a duration setting that accepts Go duration syntax
// ("5m", "90s") or a bare number of seconds ("300").
func durationSetting(lookup func(string) string, key string, def time.Duration) (time.Duration, error) {
	raw := lookup(key)
	if raw == "" {
		return def, nil
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		return time.Duration(secs) * time.Second, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q", key, raw)
	}
	return d, nil
}

// parseEnvFile reads KEY=VALUE lines. Blank lines and #-comments are
// ignored; values may be single- or double-quoted.
func parseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	vals := map[string]string{}
	scanner := bufio.NewScanner(f)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value, found := strings.Cut(text, "=")
		if !found {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		vals[key] = value
	}
	return vals, scanner.Err()
}
