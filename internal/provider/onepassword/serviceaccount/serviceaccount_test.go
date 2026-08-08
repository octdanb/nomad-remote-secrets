package serviceaccount

import (
	"testing"

	onepassword "github.com/1password/onepassword-sdk-go"
)

func strPtr(s string) *string { return &s }

func TestToOpItem(t *testing.T) {
	sdkItem := &onepassword.Item{
		ID:       "itemid",
		Title:    "database",
		Category: onepassword.ItemCategoryLogin,
		Notes:    "some notes",
		Sections: []onepassword.ItemSection{{ID: "s1", Title: "replica"}},
		Fields: []onepassword.ItemField{
			{ID: "username", Title: "username", FieldType: onepassword.ItemFieldTypeText, Value: "app"},
			{ID: "password", Title: "password", FieldType: onepassword.ItemFieldTypeConcealed, Value: "hunter2"},
			{ID: "f3", Title: "host name", FieldType: onepassword.ItemFieldTypeText, Value: "db.internal"},
			{ID: "f4", Title: "password", FieldType: onepassword.ItemFieldTypeConcealed, Value: "replica-pass", SectionID: strPtr("s1")},
		},
	}

	item := toOpItem(sdkItem)

	if item.Title != "database" || item.Category != "Login" {
		t.Errorf("item = %+v", item)
	}
	if len(item.Sections) != 1 || item.Sections[0].Label != "replica" {
		t.Errorf("sections = %+v", item.Sections)
	}

	byID := map[string]int{}
	for i, f := range item.Fields {
		byID[f.ID] = i
	}

	if f := item.Fields[byID["username"]]; f.Purpose != "USERNAME" || f.Value != "app" {
		t.Errorf("username field = %+v, want USERNAME purpose", f)
	}
	if f := item.Fields[byID["password"]]; f.Purpose != "PASSWORD" || f.SectionID != "" {
		t.Errorf("password field = %+v", f)
	}
	if f := item.Fields[byID["f4"]]; f.SectionID != "s1" || f.Purpose != "" {
		t.Errorf("sectioned field = %+v", f)
	}
	// Notes surface as a field with NOTES purpose, matching Connect.
	if f := item.Fields[byID["notesPlain"]]; f.Purpose != "NOTES" || f.Value != "some notes" {
		t.Errorf("notes field = %+v", f)
	}
}

func TestToOpItemTOTP(t *testing.T) {
	sdkItem := &onepassword.Item{
		ID: "itemid",
		Fields: []onepassword.ItemField{
			{ID: "f1", Title: "one-time password", FieldType: onepassword.ItemFieldTypeTOTP, Value: "otpauth://totp/x"},
		},
	}
	item := toOpItem(sdkItem)
	if item.Fields[0].Type != "OTP" {
		t.Errorf("TOTP field type = %q, want OTP", item.Fields[0].Type)
	}
	// Details are nil here, so no code — the value stays the otpauth URI.
	if item.Fields[0].TOTP != "" || item.Fields[0].Value != "otpauth://totp/x" {
		t.Errorf("TOTP field = %+v", item.Fields[0])
	}
}
