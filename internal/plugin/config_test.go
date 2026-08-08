package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeConfig(t *testing.T, content string) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{path}
}

func TestLoadConfigFromEnv(t *testing.T) {
	cfg, err := LoadConfig(nil, envMap(map[string]string{
		"OP_CONNECT_HOST":  "http://localhost:8080/",
		"OP_CONNECT_TOKEN": "tok",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnectHost != "http://localhost:8080" {
		t.Errorf("host = %q, want trailing slash trimmed", cfg.ConnectHost)
	}
	if cfg.Token != "tok" {
		t.Errorf("token = %q", cfg.Token)
	}
	if cfg.CacheTTL != 5*time.Minute || cfg.MaxStale != 24*time.Hour || cfg.Timeout != 30*time.Second {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if cfg.CacheDir != DefaultCacheDir {
		t.Errorf("cache dir = %q", cfg.CacheDir)
	}
}

func TestConfigFileBeatsEnvironment(t *testing.T) {
	paths := writeConfig(t, `
# host-level settings
OP_CONNECT_HOST = "http://op-connect.internal:8080"
OP_CONNECT_TOKEN = 'file-token'
`)
	cfg, err := LoadConfig(paths, envMap(map[string]string{
		"OP_CONNECT_HOST":  "http://evil.example.com",
		"OP_CONNECT_TOKEN": "env-token",
		"OP_CACHE_TTL":     "1m", // not set in file, env may fill it
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnectHost != "http://op-connect.internal:8080" {
		t.Errorf("host = %q: config file must beat process env", cfg.ConnectHost)
	}
	if cfg.Token != "file-token" {
		t.Errorf("token = %q: config file must beat process env", cfg.Token)
	}
	if cfg.CacheTTL != time.Minute {
		t.Errorf("cache TTL = %v: env should fill settings absent from the file", cfg.CacheTTL)
	}
}

func TestTokenFile(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(nil, envMap(map[string]string{
		"OP_CONNECT_HOST":       "http://localhost:8080",
		"OP_CONNECT_TOKEN_FILE": tokenPath,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "secret-token" {
		t.Errorf("token = %q, want trimmed file contents", cfg.Token)
	}
}

func TestServiceAccountTokenAloneIsComplete(t *testing.T) {
	cfg, err := LoadConfig(nil, envMap(map[string]string{
		"OP_SERVICE_ACCOUNT_TOKEN": "ops_abc123",
	}))
	if err != nil {
		t.Fatalf("service account token alone should be a valid config: %v", err)
	}
	if cfg.ServiceAccountToken != "ops_abc123" {
		t.Errorf("token = %q", cfg.ServiceAccountToken)
	}
}

func TestServiceAccountTokenFile(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("ops_fromfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(nil, envMap(map[string]string{
		"OP_SERVICE_ACCOUNT_TOKEN_FILE": tokenPath,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceAccountToken != "ops_fromfile" {
		t.Errorf("token = %q, want trimmed file contents", cfg.ServiceAccountToken)
	}
}

func TestMissingHostAndToken(t *testing.T) {
	if _, err := LoadConfig(nil, envMap(nil)); err == nil || !strings.Contains(err.Error(), "OP_CONNECT_HOST") {
		t.Errorf("err = %v, want missing-host error", err)
	}
	_, err := LoadConfig(nil, envMap(map[string]string{"OP_CONNECT_HOST": "http://x"}))
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("err = %v, want missing-token error", err)
	}
}

func TestAWSOnlyConfigIsValid(t *testing.T) {
	cfg, err := LoadConfig(nil, envMap(map[string]string{
		"AWS_REGION": "us-east-1",
	}))
	if err != nil {
		t.Fatalf("AWS-only config should be valid: %v", err)
	}
	if !cfg.AWSEnabled() || cfg.OPEnabled() {
		t.Errorf("AWSEnabled=%v OPEnabled=%v", cfg.AWSEnabled(), cfg.OPEnabled())
	}
	if !cfg.AWSDecrypt {
		t.Error("AWSDecrypt should default to true")
	}
}

func TestAWSStaticCredentialsLoaded(t *testing.T) {
	cfg, err := LoadConfig(nil, envMap(map[string]string{
		"AWS_REGION":            "us-east-1",
		"AWS_ACCESS_KEY_ID":     "AKIAEXAMPLE",
		"AWS_SECRET_ACCESS_KEY": "secret",
		"AWS_SESSION_TOKEN":     "token",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AWSAccessKeyID != "AKIAEXAMPLE" || cfg.AWSSecretAccessKey != "secret" || cfg.AWSSessionToken != "token" {
		t.Errorf("static creds not loaded: %+v", cfg)
	}
}

func TestAWSEndpointOnlyIsValid(t *testing.T) {
	cfg, err := LoadConfig(nil, envMap(map[string]string{
		"AWS_ENDPOINT_URL": "http://localhost:4566/",
		"AWS_SSM_DECRYPT":  "false",
	}))
	if err != nil {
		t.Fatalf("AWS endpoint-only config should be valid: %v", err)
	}
	if cfg.AWSEndpointURL != "http://localhost:4566" {
		t.Errorf("endpoint = %q, want trailing slash trimmed", cfg.AWSEndpointURL)
	}
	if cfg.AWSDecrypt {
		t.Error("AWS_SSM_DECRYPT=false should disable decryption")
	}
}

func TestOPOnlyStillValid(t *testing.T) {
	cfg, err := LoadConfig(nil, envMap(map[string]string{
		"OP_CONNECT_HOST":  "http://x",
		"OP_CONNECT_TOKEN": "t",
	}))
	if err != nil {
		t.Fatalf("OP-only config should be valid: %v", err)
	}
	if !cfg.OPEnabled() || cfg.AWSEnabled() {
		t.Errorf("OPEnabled=%v AWSEnabled=%v", cfg.OPEnabled(), cfg.AWSEnabled())
	}
}

func TestDurationSettings(t *testing.T) {
	base := map[string]string{
		"OP_CONNECT_HOST":  "http://x",
		"OP_CONNECT_TOKEN": "t",
	}

	base["OP_CACHE_TTL"] = "300"
	cfg, err := LoadConfig(nil, envMap(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CacheTTL != 5*time.Minute {
		t.Errorf("bare seconds not parsed: %v", cfg.CacheTTL)
	}

	base["OP_CACHE_TTL"] = "90s"
	if cfg, err = LoadConfig(nil, envMap(base)); err != nil || cfg.CacheTTL != 90*time.Second {
		t.Errorf("duration syntax not parsed: %v, %v", cfg.CacheTTL, err)
	}

	base["OP_CACHE_TTL"] = "banana"
	if _, err = LoadConfig(nil, envMap(base)); err == nil {
		t.Error("invalid duration accepted")
	}
}

func TestMaxFileBytes(t *testing.T) {
	base := map[string]string{
		"OP_CONNECT_HOST":  "http://x",
		"OP_CONNECT_TOKEN": "t",
	}

	cfg, err := LoadConfig(nil, envMap(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxFileBytes != 1<<20 {
		t.Errorf("default MaxFileBytes = %d, want %d", cfg.MaxFileBytes, 1<<20)
	}

	base["SECRET_MAX_FILE_BYTES"] = "4096"
	if cfg, err = LoadConfig(nil, envMap(base)); err != nil || cfg.MaxFileBytes != 4096 {
		t.Errorf("MaxFileBytes = %d, err = %v; want 4096", cfg.MaxFileBytes, err)
	}

	base["SECRET_MAX_FILE_BYTES"] = "0" // 0 disables the limit
	if cfg, err = LoadConfig(nil, envMap(base)); err != nil || cfg.MaxFileBytes != 0 {
		t.Errorf("MaxFileBytes = %d, err = %v; want 0", cfg.MaxFileBytes, err)
	}

	base["SECRET_MAX_FILE_BYTES"] = "banana"
	if _, err = LoadConfig(nil, envMap(base)); err == nil {
		t.Error("invalid byte count accepted")
	}
}

func TestFullyDisabledCacheClearsDir(t *testing.T) {
	cfg, err := LoadConfig(nil, envMap(map[string]string{
		"OP_CONNECT_HOST":    "http://x",
		"OP_CONNECT_TOKEN":   "t",
		"OP_CACHE_TTL":       "0",
		"OP_CACHE_MAX_STALE": "0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CacheDir != "" {
		t.Errorf("cache dir = %q, want empty when caching fully disabled", cfg.CacheDir)
	}
}

func TestMissingConfigFilesIgnored(t *testing.T) {
	_, err := LoadConfig([]string{"/nonexistent/one", "/nonexistent/two"}, envMap(map[string]string{
		"OP_CONNECT_HOST":  "http://x",
		"OP_CONNECT_TOKEN": "t",
	}))
	if err != nil {
		t.Fatalf("missing config files should be skipped: %v", err)
	}
}
