package plugin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/octdanb/nomad-secret-plugin/internal/connect"
)

const (
	vaultID = "abcdefghijklmnopqrstuvwxyz"
	itemID  = "zyxwvutsrqponmlkjihgfedcba"
)

// fakeConnect serves one vault ("Prod") holding one item ("database") with
// top-level login fields, a sectioned field, and an OTP field.
func fakeConnect(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	item := connect.Item{
		ID:       itemID,
		Title:    "database",
		Category: "LOGIN",
		Sections: []connect.Section{{ID: "s1", Label: "replica"}},
		Fields: []connect.Field{
			{ID: "f1", Label: "username", Purpose: "USERNAME", Value: "app"},
			{ID: "f2", Label: "password", Purpose: "PASSWORD", Value: "hunter2"},
			{ID: "f3", Label: "host name", Value: "db.internal"},
			{ID: "f4", Label: "password", Value: "replica-pass", Section: &connect.Section{ID: "s1"}},
			{ID: "f5", Label: "one-time password", Type: "OTP", Value: "otpauth://totp/x", TOTP: "123456"},
		},
	}

	mux.HandleFunc("GET /v1/vaults", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") == `name eq "Prod"` {
			json.NewEncoder(w).Encode([]connect.Vault{{ID: vaultID, Name: "Prod"}})
			return
		}
		json.NewEncoder(w).Encode([]connect.Vault{})
	})
	mux.HandleFunc("GET /v1/vaults/"+vaultID+"/items", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") == `title eq "database"` {
			json.NewEncoder(w).Encode([]connect.Item{{ID: itemID, Title: "database"}})
			return
		}
		json.NewEncoder(w).Encode([]connect.Item{})
	})
	mux.HandleFunc("GET /v1/vaults/"+vaultID+"/items/"+itemID, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(item)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// setupEnv points the plugin at the fake server with an isolated cache and
// no host config file.
func setupEnv(t *testing.T, host string) {
	t.Helper()
	origPaths := ConfigPaths
	ConfigPaths = []string{}
	t.Cleanup(func() { ConfigPaths = origPaths })

	t.Setenv("OP_CONNECT_HOST", host)
	t.Setenv("OP_CONNECT_TOKEN", "test-token")
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN_FILE", "")
	t.Setenv("OP_CACHE_DIR", t.TempDir())
	t.Setenv("OP_CONNECT_TOKEN_FILE", "")
	t.Setenv("OP_CACHE_TTL", "")
	t.Setenv("OP_CACHE_MAX_STALE", "")
	t.Setenv("OP_REQUEST_TIMEOUT", "2s")
}

func runFetch(t *testing.T, path string) fetchResponse {
	t.Helper()
	var stdout, stderr bytes.Buffer
	Fetch(&stdout, &stderr, path)
	var resp fetchResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("fetch stdout is not valid JSON: %v\n%s", err, stdout.String())
	}
	return resp
}

func TestFingerprint(t *testing.T) {
	var out bytes.Buffer
	if err := Fingerprint(&out); err != nil {
		t.Fatal(err)
	}
	var resp map[string]string
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["type"] != "secrets" || resp["version"] != Version {
		t.Fatalf("fingerprint = %v", resp)
	}
}

func TestFetchSingleField(t *testing.T) {
	srv := fakeConnect(t)
	setupEnv(t, srv.URL)

	resp := runFetch(t, "op://Prod/database/password")
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	if resp.Result["value"] != "hunter2" || resp.Result["password"] != "hunter2" {
		t.Fatalf("result = %v", resp.Result)
	}
}

func TestFetchSectionedField(t *testing.T) {
	srv := fakeConnect(t)
	setupEnv(t, srv.URL)

	resp := runFetch(t, "op://Prod/database/replica/password")
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	if resp.Result["value"] != "replica-pass" {
		t.Fatalf("result = %v", resp.Result)
	}
}

func TestFetchAmbiguousFieldPrefersTopLevel(t *testing.T) {
	srv := fakeConnect(t)
	setupEnv(t, srv.URL)

	// "password" exists both at top level and inside the replica section;
	// an unqualified reference must resolve to the top-level field.
	resp := runFetch(t, "op://Prod/database/password")
	if resp.Result["value"] != "hunter2" {
		t.Fatalf("result = %v, want top-level password", resp.Result)
	}
}

func TestFetchWholeItem(t *testing.T) {
	srv := fakeConnect(t)
	setupEnv(t, srv.URL)

	resp := runFetch(t, "op://Prod/database")
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	want := map[string]string{
		"username":         "app",
		"password":         "hunter2",
		"host_name":        "db.internal",
		"replica_password": "replica-pass",
	}
	for k, v := range want {
		if resp.Result[k] != v {
			t.Errorf("result[%q] = %q, want %q (full: %v)", k, resp.Result[k], v, resp.Result)
		}
	}
}

func TestFetchMultipleNamedReferences(t *testing.T) {
	srv := fakeConnect(t)
	setupEnv(t, srv.URL)

	resp := runFetch(t, `
		# everything the app needs, one secret block
		db_password = op://Prod/database/password
		db_user     = op://Prod/database/username
		db          = op://Prod/database
	`)
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	want := map[string]string{
		"db_password": "hunter2",
		"db_user":     "app",
		// whole-item entry: fields prefixed with the entry name
		"db_username":         "app",
		"db_host_name":        "db.internal",
		"db_replica_password": "replica-pass",
	}
	for k, v := range want {
		if resp.Result[k] != v {
			t.Errorf("result[%q] = %q, want %q (full: %v)", k, resp.Result[k], v, resp.Result)
		}
	}
}

func TestFetchMultiFailsClosed(t *testing.T) {
	srv := fakeConnect(t)
	setupEnv(t, srv.URL)

	// One bad entry must fail the whole fetch — a task must never start
	// with only some of its secrets resolved.
	resp := runFetch(t, "good = op://Prod/database/password\nbad = op://Prod/database/nope")
	if resp.Error == "" || !strings.Contains(resp.Error, `no field "nope"`) {
		t.Fatalf("error = %q, want missing-field message", resp.Error)
	}
	if len(resp.Result) != 0 {
		t.Fatalf("result must be empty on error, got %v", resp.Result)
	}
}

func TestFetchMultiSharesCache(t *testing.T) {
	srv := fakeConnect(t)
	setupEnv(t, srv.URL)

	first := runFetch(t, "op://Prod/database/password")
	if first.Error != "" {
		t.Fatal(first.Error)
	}

	// The same reference inside a multi-entry path must be served from
	// the cache written by the bare fetch above.
	srv.Close()
	resp := runFetch(t, "pw = op://Prod/database/password")
	if resp.Error != "" {
		t.Fatalf("cached multi fetch failed: %s", resp.Error)
	}
	if resp.Result["pw"] != "hunter2" {
		t.Fatalf("result = %v", resp.Result)
	}
}

func TestFetchOTPAttribute(t *testing.T) {
	srv := fakeConnect(t)
	setupEnv(t, srv.URL)

	resp := runFetch(t, "op://Prod/database/one-time password?attribute=otp")
	if resp.Result["value"] != "123456" {
		t.Fatalf("result = %v, want current TOTP code", resp.Result)
	}
}

func TestFetchMissingFieldError(t *testing.T) {
	srv := fakeConnect(t)
	setupEnv(t, srv.URL)

	resp := runFetch(t, "op://Prod/database/nope")
	if resp.Error == "" || !strings.Contains(resp.Error, `no field "nope"`) {
		t.Fatalf("error = %q, want missing-field message", resp.Error)
	}
	if len(resp.Result) != 0 {
		t.Fatalf("result must be empty on error, got %v", resp.Result)
	}
}

func TestFetchServesFreshCacheWithoutServer(t *testing.T) {
	srv := fakeConnect(t)
	setupEnv(t, srv.URL)

	first := runFetch(t, "op://Prod/database/password")
	if first.Error != "" {
		t.Fatal(first.Error)
	}

	srv.Close() // Connect is now unreachable; the cache must answer.
	second := runFetch(t, "op://Prod/database/password")
	if second.Error != "" {
		t.Fatalf("cached fetch failed: %s", second.Error)
	}
	if second.Result["value"] != "hunter2" {
		t.Fatalf("result = %v", second.Result)
	}
}

func TestFetchStaleFallbackWhenExpired(t *testing.T) {
	srv := fakeConnect(t)
	setupEnv(t, srv.URL)
	t.Setenv("OP_CACHE_TTL", "0") // never fresh — forces a live fetch
	t.Setenv("OP_REQUEST_TIMEOUT", "500ms")

	first := runFetch(t, "op://Prod/database/password")
	if first.Error != "" {
		t.Fatal(first.Error)
	}

	srv.Close()
	var stdout, stderr bytes.Buffer
	Fetch(&stdout, &stderr, "op://Prod/database/password")
	var resp fetchResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != "" {
		t.Fatalf("stale fallback failed: %s", resp.Error)
	}
	if resp.Result["value"] != "hunter2" {
		t.Fatalf("result = %v", resp.Result)
	}
	if !strings.Contains(stderr.String(), "serving cached value") {
		t.Fatalf("stderr should note the stale serve, got: %s", stderr.String())
	}
}

func TestFetchErrorWhenNoCacheAndNoServer(t *testing.T) {
	setupEnv(t, "http://127.0.0.1:1")
	t.Setenv("OP_REQUEST_TIMEOUT", "200ms")

	resp := runFetch(t, "op://Prod/database/password")
	if resp.Error == "" {
		t.Fatalf("expected error, got %v", resp.Result)
	}
}

func TestOTPNeverCached(t *testing.T) {
	srv := fakeConnect(t)
	setupEnv(t, srv.URL)
	t.Setenv("OP_REQUEST_TIMEOUT", "500ms")

	first := runFetch(t, "op://Prod/database/one-time password?attribute=otp")
	if first.Error != "" {
		t.Fatal(first.Error)
	}

	srv.Close()
	resp := runFetch(t, "op://Prod/database/one-time password?attribute=otp")
	if resp.Error == "" {
		t.Fatal("OTP fetch must not be served from cache")
	}
}
