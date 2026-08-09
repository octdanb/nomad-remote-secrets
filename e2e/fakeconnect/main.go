// fakeconnect is a standalone in-memory 1Password Connect stand-in for
// end-to-end tests: it serves one vault ("Testing") holding one item
// ("database"), enough for the plugin's Connect backend to resolve single
// fields, sections, and whole items. No real credentials are involved, so
// the e2e suite runs hermetically on any machine and on fork PRs.
package main

import (
	"encoding/base64"
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

	// tls is a LOGIN item carrying two FILE-type fields: a UTF-8 PEM
	// certificate and a binary keystore. appconfig is a DOCUMENT item whose
	// content is a JSON file. Together they let the file-secret e2e assert
	// text and binary delivery, values, and permissions in a container.
	tlsItemID   = "e2etlsitem00000000000000ab"
	certFileID  = "e2ecertfile0000000000000ab"
	storeFileID = "e2estorefile000000000000ab"

	cfgItemID = "e2ecfgitem00000000000000ab"
	cfgFileID = "e2ecfgfile00000000000000ab"
)

// File contents the file-secret e2e materializes into secrets/ and asserts
// on. Each is a single source of truth shared with e2e/files/run.sh, which
// reproduces them to compare values (text directly, binary via base64+sha256).
const (
	docContent = "e2e-document-content\n"                                                         // welcome.txt (text document)
	certPEM    = "-----BEGIN CERTIFICATE-----\nZTJlLXRscy1jZXJ0Cg==\n-----END CERTIFICATE-----\n" // server.pem (text file field)
	appCfgJSON = "{\"db\":{\"host\":\"db.internal.test\",\"port\":5432}}\n"                       // config.json (JSON document)

	// keystoreB64 is the base64 of the binary keystore. It is stored as
	// base64 so run.sh can reproduce the exact bytes with `base64 -d`; the
	// decoded bytes contain a NUL and 0xFF, so they are not valid UTF-8 and
	// the plugin delivers them as value_base64 only.
	keystoreB64 = "3q2+7wD/AAG71g=="
)

// keystoreBytes is the raw binary keystore content served for the file field.
var keystoreBytes = mustB64(keystoreB64)

func mustB64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

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

	// A LOGIN item with two FILE-type fields: a UTF-8 PEM certificate and a
	// binary keystore. Each field references a file by ID; the plugin fetches
	// the bytes lazily from the field's content path.
	tls := connect.Item{
		ID:       tlsItemID,
		Title:    "tls",
		Category: "LOGIN",
		Fields: []connect.Field{
			{ID: "cf1", Label: "certificate", Type: "FILE"},
			{ID: "cf2", Label: "keystore", Type: "FILE"},
		},
		Files: []connect.File{
			{ID: certFileID, Name: "server.pem", Size: len(certPEM), FieldID: "cf1",
				ContentPath: "/v1/vaults/" + vaultID + "/items/" + tlsItemID + "/files/" + certFileID + "/content"},
			{ID: storeFileID, Name: "keystore.p12", Size: len(keystoreBytes), FieldID: "cf2",
				ContentPath: "/v1/vaults/" + vaultID + "/items/" + tlsItemID + "/files/" + storeFileID + "/content"},
		},
	}

	// A Document item whose content is a JSON config file.
	appcfg := connect.Item{
		ID:       cfgItemID,
		Title:    "appconfig",
		Category: "DOCUMENT",
		Files: []connect.File{
			{ID: cfgFileID, Name: "config.json", Size: len(appCfgJSON),
				ContentPath: "/v1/vaults/" + vaultID + "/items/" + cfgItemID + "/files/" + cfgFileID + "/content"},
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
		case `title eq "tls"`:
			json.NewEncoder(w).Encode([]connect.Item{{ID: tlsItemID, Title: "tls"}})
		case `title eq "appconfig"`:
			json.NewEncoder(w).Encode([]connect.Item{{ID: cfgItemID, Title: "appconfig"}})
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

	mux.HandleFunc("GET /v1/vaults/"+vaultID+"/items/"+tlsItemID, authed(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(tls)
	}))

	mux.HandleFunc("GET /v1/vaults/"+vaultID+"/items/"+cfgItemID, authed(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(appcfg)
	}))

	mux.HandleFunc("GET /v1/vaults/"+vaultID+"/items/"+docID+"/files/"+fileID+"/content", authed(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(docContent))
	}))

	mux.HandleFunc("GET /v1/vaults/"+vaultID+"/items/"+tlsItemID+"/files/"+certFileID+"/content", authed(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(certPEM))
	}))

	mux.HandleFunc("GET /v1/vaults/"+vaultID+"/items/"+tlsItemID+"/files/"+storeFileID+"/content", authed(func(w http.ResponseWriter, r *http.Request) {
		w.Write(keystoreBytes)
	}))

	mux.HandleFunc("GET /v1/vaults/"+vaultID+"/items/"+cfgItemID+"/files/"+cfgFileID+"/content", authed(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(appCfgJSON))
	}))

	log.Printf("fake 1Password Connect listening on %s (token: %s)", *addr, token)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
