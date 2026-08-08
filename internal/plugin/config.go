package plugin

import (
	"bufio"
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
const DefaultCacheDir = "/var/cache/remote-secrets"

// Config holds everything a fetch needs. The 1Password backend fields are
// used to build the op:// provider; a service account token (direct to
// 1password.com) wins over a Connect server when both are configured.
type Config struct {
	ServiceAccountToken string        // OP_SERVICE_ACCOUNT_TOKEN / OP_SERVICE_ACCOUNT_TOKEN_FILE
	ConnectHost         string        // OP_CONNECT_HOST
	Token               string        // OP_CONNECT_TOKEN / OP_CONNECT_TOKEN_FILE
	Timeout             time.Duration // OP_REQUEST_TIMEOUT (default 30s)
	CacheDir            string        // OP_CACHE_DIR
	CacheTTL            time.Duration // OP_CACHE_TTL (default 5m, 0 disables)
	MaxStale            time.Duration // OP_CACHE_MAX_STALE (default 24h, 0 disables fallback)

	// MaxFileBytes caps the raw byte length of a file-like secret (1Password
	// document/file field, AWS binary secret). Larger content is rejected
	// rather than truncated: env-var interpolation has an OS size ceiling and
	// base64 inflates content ~33%. 0 disables the limit. SECRET_MAX_FILE_BYTES.
	MaxFileBytes int64

	// AWS Parameter Store backend fields, used to build the aws-ssm:
	// provider. AWS is enabled when a region or endpoint is set.
	AWSRegion          string // AWS_REGION
	AWSProfile         string // AWS_PROFILE
	AWSEndpointURL     string // AWS_ENDPOINT_URL (e.g. localstack)
	AWSAccessKeyID     string // AWS_ACCESS_KEY_ID (optional static creds)
	AWSSecretAccessKey string // AWS_SECRET_ACCESS_KEY
	AWSSessionToken    string // AWS_SESSION_TOKEN
	AWSDecrypt         bool   // AWS_SSM_DECRYPT (default true)

	// Source records where the settings came from — the loaded config
	// file path, or "agent environment" — so error messages can point
	// operators at the right place.
	Source string
}

// OPEnabled reports whether a complete 1Password backend is configured: a
// service account token, or a Connect host paired with a token.
func (c Config) OPEnabled() bool {
	return c.ServiceAccountToken != "" || (c.ConnectHost != "" && c.Token != "")
}

// AWSEnabled reports whether the AWS Parameter Store backend is configured. A
// region or a custom endpoint (localstack) is enough — credentials come from
// the SDK's default chain.
func (c Config) AWSEnabled() bool {
	return c.AWSRegion != "" || c.AWSEndpointURL != ""
}

// Describe names the active backend(s) for error messages and diagnostics.
// Tokens are never included.
func (c Config) Describe() string {
	var parts []string
	if c.ServiceAccountToken != "" {
		parts = append(parts, "1Password service account")
	} else if c.ConnectHost != "" {
		parts = append(parts, "1Password Connect at "+c.ConnectHost)
	}
	if c.AWSEnabled() {
		if c.AWSEndpointURL != "" {
			parts = append(parts, "AWS Parameter Store at "+c.AWSEndpointURL)
		} else {
			parts = append(parts, "AWS Parameter Store (region "+c.AWSRegion+")")
		}
	}
	if len(parts) == 0 {
		return "no backend configured"
	}
	return strings.Join(parts, ", ")
}

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
		ConnectHost:  strings.TrimRight(lookup("OP_CONNECT_HOST"), "/"),
		CacheDir:     DefaultCacheDir,
		Timeout:      30 * time.Second,
		CacheTTL:     5 * time.Minute,
		MaxStale:     24 * time.Hour,
		MaxFileBytes: 1 << 20, // 1 MiB
		Source:       source,
	}

	var err error
	if cfg.ServiceAccountToken, err = tokenSetting(lookup, "OP_SERVICE_ACCOUNT_TOKEN"); err != nil {
		return Config{}, err
	}
	if cfg.Token, err = tokenSetting(lookup, "OP_CONNECT_TOKEN"); err != nil {
		return Config{}, err
	}

	// AWS Parameter Store settings. AWS is enabled when a region or a custom
	// endpoint is set; credentials are resolved by the SDK's default chain.
	cfg.AWSRegion = lookup("AWS_REGION")
	cfg.AWSProfile = lookup("AWS_PROFILE")
	cfg.AWSEndpointURL = strings.TrimRight(lookup("AWS_ENDPOINT_URL"), "/")
	cfg.AWSAccessKeyID = lookup("AWS_ACCESS_KEY_ID")
	cfg.AWSSecretAccessKey = lookup("AWS_SECRET_ACCESS_KEY")
	cfg.AWSSessionToken = lookup("AWS_SESSION_TOKEN")
	cfg.AWSDecrypt = true
	if v := lookup("AWS_SSM_DECRYPT"); v != "" {
		cfg.AWSDecrypt = v != "false"
	}

	// Backend selection. A partially configured 1Password backend keeps its
	// specific error so operators aren't left guessing. Only when nothing at
	// all is configured do we surface the generic message — which still names
	// OP_CONNECT_HOST (and now AWS_REGION) so both paths are discoverable.
	switch {
	case cfg.ServiceAccountToken != "":
		// complete 1Password service-account backend
	case cfg.ConnectHost != "" && cfg.Token == "":
		return Config{}, fmt.Errorf("no Connect token: set OP_CONNECT_TOKEN or OP_CONNECT_TOKEN_FILE in %s or in the Nomad agent environment", ConfigPaths[0])
	case cfg.OPEnabled() || cfg.AWSEnabled():
		// complete 1Password Connect and/or AWS backend
	default:
		return Config{}, fmt.Errorf("no secret backend configured: set OP_SERVICE_ACCOUNT_TOKEN or OP_CONNECT_HOST and OP_CONNECT_TOKEN (1Password), or AWS_REGION / AWS_ENDPOINT_URL (AWS Parameter Store), in %s or in the Nomad agent environment", ConfigPaths[0])
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
	if v := lookup("SECRET_MAX_FILE_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return Config{}, fmt.Errorf("SECRET_MAX_FILE_BYTES: invalid byte count %q", v)
		}
		cfg.MaxFileBytes = n
	}
	// OP_CACHE_TTL=0 with OP_CACHE_MAX_STALE=0 means fully uncached;
	// skip the cache dir entirely so nothing is written to disk.
	if cfg.CacheTTL <= 0 && cfg.MaxStale <= 0 {
		cfg.CacheDir = ""
	}
	return cfg, nil
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
