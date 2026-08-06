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

func TestMissingHostAndToken(t *testing.T) {
	if _, err := LoadConfig(nil, envMap(nil)); err == nil || !strings.Contains(err.Error(), "OP_CONNECT_HOST") {
		t.Errorf("err = %v, want missing-host error", err)
	}
	_, err := LoadConfig(nil, envMap(map[string]string{"OP_CONNECT_HOST": "http://x"}))
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("err = %v, want missing-token error", err)
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
