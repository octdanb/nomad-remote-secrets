package onepassword

import (
	"context"
	"testing"

	"github.com/octdanb/nomad-secret-plugin/internal/provider/onepassword/opitem"
	"github.com/octdanb/nomad-secret-plugin/internal/provider/onepassword/opref"
)

// fakeSource is an in-memory Source for resolve tests.
type fakeSource struct {
	item    *opitem.Item
	content map[string][]byte // fileID -> bytes
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
