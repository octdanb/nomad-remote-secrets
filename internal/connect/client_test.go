package connect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	vaultID = "abcdefghijklmnopqrstuvwxyz"
	itemID  = "zyxwvutsrqponmlkjihgfedcba"
)

// fakeConnect serves the minimal slice of the Connect API the client uses.
func fakeConnect(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	authed := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer good-token" {
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]any{"status": 401, "message": "invalid bearer token"})
				return
			}
			next(w, r)
		}
	}

	mux.HandleFunc("GET /v1/vaults", authed(func(w http.ResponseWriter, r *http.Request) {
		filter := r.URL.Query().Get("filter")
		if filter == `name eq "Prod"` {
			json.NewEncoder(w).Encode([]Vault{{ID: vaultID, Name: "Prod"}})
			return
		}
		json.NewEncoder(w).Encode([]Vault{})
	}))

	mux.HandleFunc("GET /v1/vaults/"+vaultID, authed(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Vault{ID: vaultID, Name: "Prod"})
	}))

	mux.HandleFunc("GET /v1/vaults/"+vaultID+"/items", authed(func(w http.ResponseWriter, r *http.Request) {
		filter := r.URL.Query().Get("filter")
		if filter == `title eq "database"` {
			json.NewEncoder(w).Encode([]Item{{ID: itemID, Title: "database"}})
			return
		}
		if filter == `title eq "twins"` {
			json.NewEncoder(w).Encode([]Item{{ID: itemID, Title: "twins"}, {ID: vaultID, Title: "twins"}})
			return
		}
		json.NewEncoder(w).Encode([]Item{})
	}))

	mux.HandleFunc("GET /v1/vaults/"+vaultID+"/items/"+itemID, authed(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Item{
			ID:    itemID,
			Title: "database",
			Fields: []Field{
				{ID: "f1", Label: "username", Purpose: "USERNAME", Value: "app"},
				{ID: "f2", Label: "password", Purpose: "PASSWORD", Value: "hunter2"},
			},
		})
	}))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGetVaultByName(t *testing.T) {
	srv := fakeConnect(t)
	c := New(srv.URL, "good-token", time.Second)

	v, err := c.GetVault(context.Background(), "Prod")
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != vaultID {
		t.Fatalf("vault ID = %q, want %q", v.ID, vaultID)
	}
}

func TestGetVaultByID(t *testing.T) {
	srv := fakeConnect(t)
	c := New(srv.URL, "good-token", time.Second)

	v, err := c.GetVault(context.Background(), vaultID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Name != "Prod" {
		t.Fatalf("vault name = %q, want Prod", v.Name)
	}
}

func TestGetVaultNotFound(t *testing.T) {
	srv := fakeConnect(t)
	c := New(srv.URL, "good-token", time.Second)

	_, err := c.GetVault(context.Background(), "Missing")
	if err == nil || !strings.Contains(err.Error(), `no vault named "Missing"`) {
		t.Fatalf("err = %v, want no-vault error", err)
	}
}

func TestGetItemByTitleFetchesFullItem(t *testing.T) {
	srv := fakeConnect(t)
	c := New(srv.URL, "good-token", time.Second)

	it, err := c.GetItem(context.Background(), vaultID, "database")
	if err != nil {
		t.Fatal(err)
	}
	if len(it.Fields) != 2 || it.Fields[1].Value != "hunter2" {
		t.Fatalf("full item not fetched: %+v", it)
	}
}

func TestGetItemAmbiguous(t *testing.T) {
	srv := fakeConnect(t)
	c := New(srv.URL, "good-token", time.Second)

	_, err := c.GetItem(context.Background(), vaultID, "twins")
	if err == nil || !strings.Contains(err.Error(), "2 items") {
		t.Fatalf("err = %v, want ambiguity error", err)
	}
}

func TestAPIErrorMessageSurfaced(t *testing.T) {
	srv := fakeConnect(t)
	c := New(srv.URL, "bad-token", time.Second)

	_, err := c.GetVault(context.Background(), "Prod")
	if err == nil || !strings.Contains(err.Error(), "invalid bearer token") {
		t.Fatalf("err = %v, want Connect error message", err)
	}
}

func TestConnectionErrorMentionsHost(t *testing.T) {
	c := New("http://127.0.0.1:1", "tok", 200*time.Millisecond)
	_, err := c.GetVault(context.Background(), "Prod")
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("err = %v, want connection error naming the host", err)
	}
}
