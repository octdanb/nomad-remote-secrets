package opref

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Ref
	}{
		{"field", "op://Prod/database/password", Ref{Vault: "Prod", Item: "database", Field: "password"}},
		{"whole item", "op://Prod/database", Ref{Vault: "Prod", Item: "database"}},
		{"section", "op://Prod/api/tokens/publish", Ref{Vault: "Prod", Item: "api", Section: "tokens", Field: "publish"}},
		{"no scheme", "Prod/database/password", Ref{Vault: "Prod", Item: "database", Field: "password"}},
		{"spaces kept", "op://My Vault/db server/password", Ref{Vault: "My Vault", Item: "db server", Field: "password"}},
		{"percent decoded", "op://My%20Vault/db/password", Ref{Vault: "My Vault", Item: "db", Field: "password"}},
		{"otp attribute", "op://Prod/mfa/one-time password?attribute=otp", Ref{Vault: "Prod", Item: "mfa", Field: "one-time password", Attribute: "otp"}},
		{"file attribute on item", "op://Prod/mydoc?attribute=file", Ref{Vault: "Prod", Item: "mydoc", Attribute: "file"}},
		{"file attribute on field", "op://Prod/item/cert?attribute=file", Ref{Vault: "Prod", Item: "item", Field: "cert", Attribute: "file"}},
		{"encoding base64", "op://Prod/mydoc?encoding=base64", Ref{Vault: "Prod", Item: "mydoc", Encoding: "base64"}},
		{"attribute and encoding", "op://Prod/item/cert?attribute=file&encoding=base64", Ref{Vault: "Prod", Item: "item", Field: "cert", Attribute: "file", Encoding: "base64"}},
		{"surrounding space", "  op://Prod/database/password  ", Ref{Vault: "Prod", Item: "database", Field: "password"}},
		{"id segments", "op://abcdefghijklmnopqrstuvwxyz/item-id/credential", Ref{Vault: "abcdefghijklmnopqrstuvwxyz", Item: "item-id", Field: "credential"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.in)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("Parse(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"vault only", "op://Prod"},
		{"too many segments", "op://a/b/c/d/e"},
		{"empty segment", "op://Prod//password"},
		{"wrong scheme", "vault://Prod/db/password"},
		{"unknown attribute", "op://Prod/db/password?attribute=ssh"},
		{"otp on whole item", "op://Prod/db?attribute=otp"},
		{"unknown encoding", "op://Prod/db?encoding=hex"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := Parse(tc.in); err == nil {
				t.Fatalf("Parse(%q) = %+v, want error", tc.in, got)
			}
		})
	}
}

func TestWholeItem(t *testing.T) {
	whole, _ := Parse("op://v/i")
	if !whole.WholeItem() {
		t.Error("two-segment ref should be whole-item")
	}
	field, _ := Parse("op://v/i/f")
	if field.WholeItem() {
		t.Error("three-segment ref should not be whole-item")
	}
}

func TestString(t *testing.T) {
	for _, in := range []string{
		"op://Prod/database/password",
		"op://Prod/api/tokens/publish",
		"op://Prod/mfa/code?attribute=otp",
		"op://Prod/mydoc?attribute=file",
		"op://Prod/mydoc?encoding=base64",
	} {
		ref, err := Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		if ref.String() != in {
			t.Errorf("String() = %q, want %q", ref.String(), in)
		}
	}
}
