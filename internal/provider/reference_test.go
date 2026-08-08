package provider

import "testing"

func TestSplitEntriesSingle(t *testing.T) {
	for _, in := range []string{
		"op://Prod/database/password",
		"op://Prod/db, with a comma/password", // commas never split a bare ref
		"Prod/database/password",
	} {
		entries, err := SplitEntries(in)
		if err != nil {
			t.Fatalf("SplitEntries(%q) error: %v", in, err)
		}
		if len(entries) != 1 || entries[0].Name != "" {
			t.Fatalf("SplitEntries(%q) = %+v, want one unnamed entry", in, entries)
		}
	}
}

func TestSplitEntriesMulti(t *testing.T) {
	in := `
		# database credentials
		db_password = op://Production/database/password
		api_key     = op://Production/api/credential
		twilio      = op://Production/twilio-prod
	`
	entries, err := SplitEntries(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(entries), entries)
	}
	if entries[0].Name != "db_password" || entries[0].Ref != "op://Production/database/password" {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if entries[2].Name != "twilio" || entries[2].Ref != "op://Production/twilio-prod" {
		t.Errorf("entry 2 = %+v", entries[2])
	}
}

func TestSplitEntriesCommaSeparated(t *testing.T) {
	entries, err := SplitEntries("a = op://P/x/f, b = op://P/y/g")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[1].Name != "b" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestSplitEntriesErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"duplicate name", "a = op://P/x/f\na = op://P/y/g"},
		{"invalid name", "a = op://P/x/f\nbad-name = op://P/y/g"},
		{"only comments", "# nothing = op://here"},
		{"multiple unnamed", "op://P/x/f\nop://P/y/g"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := SplitEntries(tc.in); err == nil {
				t.Fatalf("SplitEntries(%q) = %+v, want error", tc.in, got)
			}
		})
	}
}

func TestScheme(t *testing.T) {
	cases := map[string]string{
		"op://Prod/db/pw":     "op",
		"aws-ssm:/prod/db":    "aws-ssm",
		"aws-sm:prod/db/cred": "aws-sm",
		"Prod/db/pw":          "",
		"":                    "",
	}
	for in, want := range cases {
		if got := Scheme(in); got != want {
			t.Errorf("Scheme(%q) = %q, want %q", in, got, want)
		}
	}
}
