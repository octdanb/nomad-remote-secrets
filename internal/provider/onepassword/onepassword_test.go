package onepassword

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/octdanb/nomad-remote-secrets/internal/provider/onepassword/opitem"
	"github.com/octdanb/nomad-remote-secrets/internal/provider/onepassword/opref"
)

// fakeSource is an in-memory Source for resolve tests.
type fakeSource struct {
	item       *opitem.Item
	content    map[string][]byte // fileID -> bytes
	contentErr map[string]error  // fileID -> error returned by GetFileContent
}

func (f *fakeSource) GetVault(ctx context.Context, nameOrID string) (*opitem.Vault, error) {
	return &opitem.Vault{ID: "v1", Name: "Prod"}, nil
}

func (f *fakeSource) GetItem(ctx context.Context, vaultID, nameOrID string) (*opitem.Item, error) {
	return f.item, nil
}

func (f *fakeSource) ListVaults(ctx context.Context) ([]opitem.Vault, error) {
	return []opitem.Vault{{ID: "v1", Name: "Prod"}}, nil
}

func (f *fakeSource) GetFileContent(ctx context.Context, vaultID, itemID, fileID string) ([]byte, error) {
	if err := f.contentErr[fileID]; err != nil {
		return nil, err
	}
	return f.content[fileID], nil
}

func mustResolve(t *testing.T, src Source, ref string, max int64) map[string]string {
	t.Helper()
	r, err := opref.Parse(ref)
	if err != nil {
		t.Fatalf("parse %q: %v", ref, err)
	}
	res, err := resolve(context.Background(), src, r, max)
	if err != nil {
		t.Fatalf("resolve %q: %v", ref, err)
	}
	return res.Values
}

func TestResolveDocumentItem(t *testing.T) {
	src := &fakeSource{
		item: &opitem.Item{
			ID: "i1", Title: "mydoc", Category: "DOCUMENT",
			Files: []opitem.File{{ID: "d1", Name: "notes.txt", Size: 5}},
		},
		content: map[string][]byte{"d1": []byte("hello")},
	}
	v := mustResolve(t, src, "op://Prod/mydoc", 0)
	if v["value"] != "hello" {
		t.Errorf("value = %q, want hello", v["value"])
	}
	if v["value_base64"] != "aGVsbG8=" {
		t.Errorf("value_base64 = %q", v["value_base64"])
	}
	if v["filename"] != "notes.txt" {
		t.Errorf("filename = %q", v["filename"])
	}
}

func TestResolveBinaryFileField(t *testing.T) {
	src := &fakeSource{
		item: &opitem.Item{
			ID: "i1", Title: "item", Category: "LOGIN",
			Fields: []opitem.Field{{ID: "f1", Label: "cert", Type: "FILE", FileID: "b1"}},
			Files:  []opitem.File{{ID: "b1", Name: "key.p12", FieldID: "f1", Size: 4}},
		},
		content: map[string][]byte{"b1": {0xff, 0xfe, 0x00, 0x01}},
	}
	v := mustResolve(t, src, "op://Prod/item/cert", 0)
	if _, ok := v["value"]; ok {
		t.Error("binary file must not set value")
	}
	if v["value_base64"] == "" {
		t.Error("value_base64 missing")
	}
	if v["filename"] != "key.p12" {
		t.Errorf("filename = %q", v["filename"])
	}
}

func TestResolveFileEncodingBase64(t *testing.T) {
	src := &fakeSource{
		item: &opitem.Item{
			ID: "i1", Title: "mydoc", Category: "DOCUMENT",
			Files: []opitem.File{{ID: "d1", Name: "notes.txt", Size: 5}},
		},
		content: map[string][]byte{"d1": []byte("hello")},
	}
	v := mustResolve(t, src, "op://Prod/mydoc?encoding=base64", 0)
	if _, ok := v["value"]; ok {
		t.Error("encoding=base64 must drop value")
	}
	if v["value_base64"] != "aGVsbG8=" {
		t.Errorf("value_base64 = %q", v["value_base64"])
	}
}

func TestResolveFileSizeLimit(t *testing.T) {
	src := &fakeSource{
		item: &opitem.Item{
			ID: "i1", Title: "mydoc", Category: "DOCUMENT",
			Files: []opitem.File{{ID: "d1", Name: "big.bin", Size: 100}},
		},
		content: map[string][]byte{"d1": make([]byte, 100)},
	}
	r, _ := opref.Parse("op://Prod/mydoc")
	if _, err := resolve(context.Background(), src, r, 10); err == nil {
		t.Fatal("expected size-limit error")
	}
}

func TestResolveAttributeFileOnField(t *testing.T) {
	// A field whose type isn't FILE but forced with ?attribute=file. The
	// field carries no FileID, so the file is matched by FieldID.
	src := &fakeSource{
		item: &opitem.Item{
			ID: "i1", Title: "item", Category: "LOGIN",
			Fields: []opitem.Field{{ID: "f1", Label: "cert"}},
			Files:  []opitem.File{{ID: "b1", Name: "cert.pem", FieldID: "f1", Size: 4}},
		},
		content: map[string][]byte{"b1": []byte("PEM!")},
	}
	v := mustResolve(t, src, "op://Prod/item/cert?attribute=file", 0)
	if v["value"] != "PEM!" || v["filename"] != "cert.pem" {
		t.Errorf("unexpected: %+v", v)
	}
}

func TestCacheScopeSeparatesBackends(t *testing.T) {
	connect := (&Provider{cfg: Config{ConnectHost: "http://c:8080", Token: "tok"}}).cacheScope()
	sa := (&Provider{cfg: Config{ConnectHost: "http://c:8080", Token: "tok", ServiceAccountToken: "ops_x"}}).cacheScope()
	if connect == sa {
		t.Error("connect and service-account configs must not share cache entries")
	}
	sa2 := (&Provider{cfg: Config{ServiceAccountToken: "ops_y"}}).cacheScope()
	if sa == sa2 {
		t.Error("different service account tokens must not share cache entries")
	}
}

// --- Test fixtures for file and field-type coverage ---------------------

// docSource builds a Source with a single DOCUMENT item whose whole-item
// reference resolves to one document file.
func docSource(name string, data []byte) *fakeSource {
	return &fakeSource{
		item: &opitem.Item{
			ID: "i1", Title: "doc", Category: "DOCUMENT",
			Files: []opitem.File{{ID: "d1", Name: name, Size: len(data)}},
		},
		content: map[string][]byte{"d1": data},
	}
}

// fieldFileSource builds a Source with a LOGIN item exposing one FILE-type
// field backed by an attachment.
func fieldFileSource(label, name string, data []byte) *fakeSource {
	return &fakeSource{
		item: &opitem.Item{
			ID: "i1", Title: "item", Category: "LOGIN",
			Fields: []opitem.Field{{ID: "f1", Label: label, Type: "FILE", FileID: "b1"}},
			Files:  []opitem.File{{ID: "b1", Name: name, FieldID: "f1", Size: len(data)}},
		},
		content: map[string][]byte{"b1": data},
	}
}

func resolveErr(t *testing.T, src Source, ref string, max int64) error {
	t.Helper()
	r, err := opref.Parse(ref)
	if err != nil {
		return err
	}
	_, err = resolve(context.Background(), src, r, max)
	return err
}

// TestResolveFileTypesPass exercises the full range of file payloads a
// reference may resolve to: text (PEM, JSON), binary (PKCS#12, PNG),
// multi-byte UTF-8, empty files, and files forced via ?attribute=file. Text
// files expose a "value"; binary files expose only "value_base64". Every
// case must carry the exact bytes at "value_base64" and the original filename.
func TestResolveFileTypesPass(t *testing.T) {
	pem := []byte("-----BEGIN CERTIFICATE-----\nMIIBdummy\n-----END CERTIFICATE-----\n")
	jsonData := []byte(`{"api_key":"abc","nested":{"x":1}}`)
	multibyte := []byte("café 🔑 — configuration lines\n")
	p12 := []byte{0x30, 0x82, 0x04, 0x00, 0xff, 0xfe, 0x00} // DER, invalid UTF-8
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0xff}

	cases := []struct {
		name     string
		src      *fakeSource
		ref      string
		data     []byte
		wantText bool // whether the "value" key should be present
		filename string
	}{
		{"pem cert as document", docSource("bundle.pem", pem), "op://Prod/doc", pem, true, "bundle.pem"},
		{"pem cert as file field", fieldFileSource("cert", "server.pem", pem), "op://Prod/item/cert", pem, true, "server.pem"},
		{"json config document", docSource("config.json", jsonData), "op://Prod/doc", jsonData, true, "config.json"},
		{"multibyte utf8 document", docSource("notes.txt", multibyte), "op://Prod/doc", multibyte, true, "notes.txt"},
		{"empty file", docSource("empty.txt", []byte{}), "op://Prod/doc", []byte{}, true, "empty.txt"},
		{"pkcs12 keystore binary", docSource("store.p12", p12), "op://Prod/doc", p12, false, "store.p12"},
		{"png binary file field", fieldFileSource("logo", "logo.png", png), "op://Prod/item/logo", png, false, "logo.png"},
		{"forced file on whole document", docSource("README", []byte("hi there")), "op://Prod/doc?attribute=file", []byte("hi there"), true, "README"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := mustResolve(t, tc.src, tc.ref, 0)

			wantB64 := base64.StdEncoding.EncodeToString(tc.data)
			if v["value_base64"] != wantB64 {
				t.Errorf("value_base64 = %q, want %q", v["value_base64"], wantB64)
			}
			if v["filename"] != tc.filename {
				t.Errorf("filename = %q, want %q", v["filename"], tc.filename)
			}
			got, hasValue := v["value"]
			if tc.wantText {
				if !hasValue {
					t.Errorf("text file must expose a value key")
				} else if got != string(tc.data) {
					t.Errorf("value = %q, want %q", got, tc.data)
				}
			} else if hasValue {
				t.Errorf("binary file must not expose a value key, got %q", got)
			}
		})
	}
}

// TestResolveFileErrors covers every failing file path: size guardrails
// (declared size and actual bytes), missing or ambiguous file content,
// unattached file fields, and a permission error while fetching bytes. The
// task must fail closed on all of them.
func TestResolveFileErrors(t *testing.T) {
	permErr := errors.New("403 Forbidden: token lacks read access to file")

	// A document whose declared size is a lie: it claims 1 byte but the
	// backend returns 100. The post-fetch guard must still reject it.
	sneaky := &fakeSource{
		item: &opitem.Item{
			ID: "i1", Title: "doc", Category: "DOCUMENT",
			Files: []opitem.File{{ID: "d1", Name: "sneaky.bin", Size: 1}},
		},
		content: map[string][]byte{"d1": make([]byte, 100)},
	}

	// A document item whose GetFileContent is denied — simulates a scoped
	// token that can read the item metadata but not the file bytes.
	denied := &fakeSource{
		item: &opitem.Item{
			ID: "i1", Title: "doc", Category: "DOCUMENT",
			Files: []opitem.File{{ID: "d1", Name: "secret.txt", Size: 4}},
		},
		content:    map[string][]byte{"d1": []byte("data")},
		contentErr: map[string]error{"d1": permErr},
	}

	cases := []struct {
		name    string
		src     *fakeSource
		ref     string
		max     int64
		wantErr string
	}{
		{
			"declared size exceeds limit",
			docSource("big.bin", make([]byte, 100)),
			"op://Prod/doc", 10, "exceeding SECRET_MAX_FILE_BYTES",
		},
		{
			"actual bytes exceed limit despite small declared size",
			sneaky,
			"op://Prod/doc", 10, "exceeding SECRET_MAX_FILE_BYTES",
		},
		{
			"document item has no file content",
			&fakeSource{item: &opitem.Item{ID: "i1", Title: "doc", Category: "DOCUMENT"}},
			"op://Prod/doc", 0, "has no file content",
		},
		{
			"document item has multiple files",
			&fakeSource{item: &opitem.Item{
				ID: "i1", Title: "doc", Category: "DOCUMENT",
				Files: []opitem.File{{ID: "d1", Name: "a.txt"}, {ID: "d2", Name: "b.txt"}},
			}},
			"op://Prod/doc", 0, "multiple files",
		},
		{
			"file field has no attached file",
			&fakeSource{item: &opitem.Item{
				ID: "i1", Title: "item", Category: "LOGIN",
				Fields: []opitem.Field{{ID: "f1", Label: "cert", Type: "FILE", FileID: "missing"}},
			}},
			"op://Prod/item/cert", 0, "has no attached file",
		},
		{
			"forced file on a field with no attachment",
			&fakeSource{item: &opitem.Item{
				ID: "i1", Title: "item", Category: "LOGIN",
				Fields: []opitem.Field{{ID: "f1", Label: "note"}},
			}},
			"op://Prod/item/note?attribute=file", 0, "has no attached file",
		},
		{
			"permission denied fetching file bytes",
			denied,
			"op://Prod/doc", 0, "403 Forbidden",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := resolveErr(t, tc.src, tc.ref, tc.max)
			if err == nil {
				t.Fatalf("resolve(%q) = nil error, want %q", tc.ref, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// fieldTypesItem is a login item covering the field types a single-field
// reference may address: STRING, CONCEALED (password), a purpose-tagged
// username, a labelled token needing key sanitization, and a sectioned field.
func fieldTypesItem() *opitem.Item {
	return &opitem.Item{
		ID: "i1", Title: "login", Category: "LOGIN",
		Sections: []opitem.Section{{ID: "s1", Label: "replica"}},
		Fields: []opitem.Field{
			{ID: "f1", Label: "username", Purpose: "USERNAME", Type: "STRING", Value: "app"},
			{ID: "f2", Label: "password", Purpose: "PASSWORD", Type: "CONCEALED", Value: "hunter2"},
			{ID: "f3", Label: "API Token", Type: "CONCEALED", Value: "tok-123"},
			{ID: "f4", Label: "host name", Type: "STRING", Value: "db.internal"},
			{ID: "f5", Label: "password", Type: "CONCEALED", Value: "replica-pass", SectionID: "s1"},
		},
	}
}

// TestResolveFieldTypesPass checks that every password/field type resolves to
// the expected value, that "value" is always the stable key, and that a
// sanitized label alias is exposed alongside it.
func TestResolveFieldTypesPass(t *testing.T) {
	src := &fakeSource{item: fieldTypesItem()}

	cases := []struct{ name, ref, key, want string }{
		{"string field by label", "op://Prod/login/username", "value", "app"},
		{"concealed password value", "op://Prod/login/password", "value", "hunter2"},
		{"concealed password alias", "op://Prod/login/password", "password", "hunter2"},
		{"field by purpose", "op://Prod/login/USERNAME", "value", "app"},
		{"sanitized label alias", "op://Prod/login/API Token", "API_Token", "tok-123"},
		{"sectioned password", "op://Prod/login/replica/password", "value", "replica-pass"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := mustResolve(t, src, tc.ref, 0)
			if v[tc.key] != tc.want {
				t.Errorf("resolve(%q)[%q] = %q, want %q", tc.ref, tc.key, v[tc.key], tc.want)
			}
			if v["value"] == "" && tc.key != "value" {
				t.Errorf("resolve(%q) must always set the stable value key", tc.ref)
			}
		})
	}
}

// TestResolveWholeItemExpansion checks a whole-item reference flattens every
// field, prefixes sectioned fields, sanitizes labels, and guarantees the
// conventional username/password keys.
func TestResolveWholeItemExpansion(t *testing.T) {
	src := &fakeSource{item: fieldTypesItem()}
	v := mustResolve(t, src, "op://Prod/login", 0)

	want := map[string]string{
		"username":         "app",
		"password":         "hunter2", // top-level wins over the sectioned one
		"host_name":        "db.internal",
		"API_Token":        "tok-123",
		"replica_password": "replica-pass",
	}
	for k, exp := range want {
		if v[k] != exp {
			t.Errorf("whole-item[%q] = %q, want %q (full: %v)", k, v[k], exp, v)
		}
	}
}

// TestResolveFieldErrors covers the failing single-field paths: an unknown
// field and an ambiguous field that needs a section qualifier.
func TestResolveFieldErrors(t *testing.T) {
	ambiguous := &fakeSource{item: &opitem.Item{
		ID: "i1", Title: "login", Category: "LOGIN",
		Sections: []opitem.Section{{ID: "s1", Label: "a"}, {ID: "s2", Label: "b"}},
		Fields: []opitem.Field{
			{ID: "f1", Label: "token", Value: "x", SectionID: "s1"},
			{ID: "f2", Label: "token", Value: "y", SectionID: "s2"},
		},
	}}

	cases := []struct {
		name    string
		src     *fakeSource
		ref     string
		wantErr string
	}{
		{"unknown field", &fakeSource{item: fieldTypesItem()}, "op://Prod/login/nope", `no field "nope"`},
		{"ambiguous field needs section", ambiguous, "op://Prod/login/token", "ambiguous"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := resolveErr(t, tc.src, tc.ref, 0)
			if err == nil {
				t.Fatalf("resolve(%q) = nil error, want %q", tc.ref, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
