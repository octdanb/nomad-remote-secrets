// fakeconnect is a standalone in-memory 1Password Connect stand-in for
// end-to-end tests: it serves one vault ("Testing") holding one item
// ("database"), enough for the plugin's Connect backend to resolve single
// fields, sections, and whole items. No real credentials are involved, so
// the e2e suite runs hermetically on any machine and on fork PRs.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"

	"github.com/octdanb/nomad-remote-secrets/internal/provider/onepassword/connect"
)

const (
	vaultID = "e2evaultid00000000000000ab"
	itemID  = "e2eitemid000000000000000ab"
	docID   = "e2edocitem00000000000000ab"
	fileID  = "e2efileid000000000000000ab"
	token   = "e2e-test-token"
)

// docContent is the body of the "welcome" document item; the e2e job
// materializes it into secrets/ via a template and asserts on it.
const docContent = "e2e-document-content\n"

func main() {
	addr := flag.String("addr", "127.0.0.1:8999", "listen address")
	flag.Parse()

	item := connect.Item{
		ID:       itemID,
		Title:    "database",
		Category: "LOGIN",
		Sections: []connect.Section{{ID: "s1", Label: "replica"}},
		Fields: []connect.Field{
			{ID: "f1", Label: "username", Purpose: "USERNAME", Value: "app-user"},
			{ID: "f2", Label: "password", Purpose: "PASSWORD", Value: "hunter2-e2e"},
			{ID: "f3", Label: "host name", Value: "db.internal.test"},
			{ID: "f4", Label: "password", Value: "replica-pass-e2e", Section: &connect.Section{ID: "s1"}},
		},
	}

	// A Document item whose content is fetched as a file-like secret.
	doc := connect.Item{
		ID:       docID,
		Title:    "welcome",
		Category: "DOCUMENT",
		Files: []connect.File{
			{ID: fileID, Name: "welcome.txt", Size: len(docContent),
				ContentPath: "/v1/vaults/" + vaultID + "/items/" + docID + "/files/" + fileID + "/content"},
		},
	}

	mux := http.NewServeMux()

	authed := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+token {
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]any{"status": 401, "message": "invalid bearer token"})
				return
			}
			log.Printf("%s %s", r.Method, r.URL)
			next(w, r)
		}
	}

	mux.HandleFunc("GET /v1/vaults", authed(func(w http.ResponseWriter, r *http.Request) {
		filter := r.URL.Query().Get("filter")
		if filter == "" || filter == `name eq "Testing"` {
			json.NewEncoder(w).Encode([]connect.Vault{{ID: vaultID, Name: "Testing"}})
			return
		}
		json.NewEncoder(w).Encode([]connect.Vault{})
	}))

	mux.HandleFunc("GET /v1/vaults/"+vaultID+"/items", authed(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("filter") {
		case `title eq "database"`:
			json.NewEncoder(w).Encode([]connect.Item{{ID: itemID, Title: "database"}})
		case `title eq "welcome"`:
			json.NewEncoder(w).Encode([]connect.Item{{ID: docID, Title: "welcome"}})
		default:
			json.NewEncoder(w).Encode([]connect.Item{})
		}
	}))

	mux.HandleFunc("GET /v1/vaults/"+vaultID+"/items/"+itemID, authed(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(item)
	}))

	mux.HandleFunc("GET /v1/vaults/"+vaultID+"/items/"+docID, authed(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(doc)
	}))

	mux.HandleFunc("GET /v1/vaults/"+vaultID+"/items/"+docID+"/files/"+fileID+"/content", authed(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(docContent))
	}))

	log.Printf("fake 1Password Connect listening on %s (token: %s)", *addr, token)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
