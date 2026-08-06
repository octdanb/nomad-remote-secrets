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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := Parse(tc.in); err == nil {
				t.Fatalf("Parse(%q) = %+v, want error", tc.in, got)
			}
		})
	}
}

func TestParseAllSingle(t *testing.T) {
	for _, in := range []string{
		"op://Prod/database/password",
		"op://Prod/db, with a comma/password", // commas never split a bare ref
		"Prod/database/password",
	} {
		entries, err := ParseAll(in)
		if err != nil {
			t.Fatalf("ParseAll(%q) error: %v", in, err)
		}
		if len(entries) != 1 || entries[0].Name != "" {
			t.Fatalf("ParseAll(%q) = %+v, want one unnamed entry", in, entries)
		}
	}
}

func TestParseAllMulti(t *testing.T) {
	in := `
		# database credentials
		db_password = op://Production/database/password
		api_key     = op://Production/api/credential
		twilio      = op://Production/twilio-prod
	`
	entries, err := ParseAll(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(entries), entries)
	}
	if entries[0].Name != "db_password" || entries[0].Ref.Field != "password" {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if entries[2].Name != "twilio" || !entries[2].Ref.WholeItem() {
		t.Errorf("entry 2 = %+v, want whole-item ref", entries[2])
	}
}

func TestParseAllCommaSeparated(t *testing.T) {
	entries, err := ParseAll("a = op://P/x/f, b = op://P/y/g")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[1].Name != "b" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestParseAllErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"duplicate name", "a = op://P/x/f\na = op://P/y/g"},
		{"invalid name", "a = op://P/x/f\nbad-name = op://P/y/g"},
		{"bad ref in entry", "a = op://P"},
		{"only comments", "# nothing = op://here"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := ParseAll(tc.in); err == nil {
				t.Fatalf("ParseAll(%q) = %+v, want error", tc.in, got)
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
