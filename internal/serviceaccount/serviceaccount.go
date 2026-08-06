// Package serviceaccount resolves secrets directly against 1password.com
// using a 1Password service account token (ops_...) via the official Go SDK
// — no self-hosted Connect server required. The SDK runs 1Password's core as
// an embedded WebAssembly module, so the plugin remains a CGO-free static
// binary.
package serviceaccount

import (
	"context"
	"fmt"
	"strings"

	onepassword "github.com/1password/onepassword-sdk-go"

	"github.com/octdanb/nomad-secret-plugin/internal/opitem"
)

// Source resolves vaults and items with a service account token.
type Source struct {
	client *onepassword.Client
}

// New authenticates the SDK client. integrationVersion is reported to
// 1Password for audit/telemetry purposes.
func New(ctx context.Context, token, integrationVersion string) (*Source, error) {
	client, err := onepassword.NewClient(ctx,
		onepassword.WithServiceAccountToken(token),
		onepassword.WithIntegrationInfo("nomad-secret-plugin", integrationVersion),
	)
	if err != nil {
		return nil, fmt.Errorf("authenticating 1Password service account: %w", err)
	}
	return &Source{client: client}, nil
}

// GetVault resolves a vault by ID or exact title among the vaults the
// service account is granted.
func (s *Source) GetVault(ctx context.Context, nameOrID string) (*opitem.Vault, error) {
	overviews, err := s.client.Vaults().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing vaults: %w", err)
	}

	var matches []*opitem.Vault
	for _, v := range overviews {
		if v.ID == nameOrID {
			return &opitem.Vault{ID: v.ID, Name: v.Title}, nil
		}
		if strings.EqualFold(v.Title, nameOrID) {
			matches = append(matches, &opitem.Vault{ID: v.ID, Name: v.Title})
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no vault named %q is visible to this service account", nameOrID)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("%d vaults named %q; reference the vault by ID instead", len(matches), nameOrID)
	}
}

// GetItem resolves an item within a vault by ID or exact title and returns
// it with its full field values.
func (s *Source) GetItem(ctx context.Context, vaultID, nameOrID string) (*opitem.Item, error) {
	overviews, err := s.client.Items().List(ctx, vaultID)
	if err != nil {
		return nil, fmt.Errorf("listing items: %w", err)
	}

	itemID := ""
	var titleMatches []string
	for _, it := range overviews {
		if it.ID == nameOrID {
			itemID = it.ID
			break
		}
		if strings.EqualFold(it.Title, nameOrID) {
			titleMatches = append(titleMatches, it.ID)
		}
	}
	if itemID == "" {
		switch len(titleMatches) {
		case 0:
			return nil, fmt.Errorf("no item titled %q in vault", nameOrID)
		case 1:
			itemID = titleMatches[0]
		default:
			return nil, fmt.Errorf("%d items titled %q in vault; reference the item by ID instead", len(titleMatches), nameOrID)
		}
	}

	item, err := s.client.Items().Get(ctx, vaultID, itemID)
	if err != nil {
		return nil, fmt.Errorf("reading item: %w", err)
	}
	return toOpItem(&item), nil
}

// toOpItem converts an SDK item into the backend-neutral model.
func toOpItem(it *onepassword.Item) *opitem.Item {
	out := &opitem.Item{
		ID:       it.ID,
		Title:    it.Title,
		Category: string(it.Category),
	}
	for _, s := range it.Sections {
		out.Sections = append(out.Sections, opitem.Section{ID: s.ID, Label: s.Title})
	}
	for _, f := range it.Fields {
		nf := opitem.Field{
			ID:      f.ID,
			Label:   f.Title,
			Type:    string(f.FieldType),
			Purpose: purposeForFieldID(f.ID),
			Value:   f.Value,
		}
		if f.SectionID != nil {
			nf.SectionID = *f.SectionID
		}
		if f.FieldType == onepassword.ItemFieldTypeTOTP {
			nf.Type = "OTP"
			if f.Details != nil {
				if otp := f.Details.OTP(); otp != nil && otp.Code != nil {
					nf.TOTP = *otp.Code
				}
			}
		}
		out.Fields = append(out.Fields, nf)
	}
	// The SDK surfaces the notes field as a top-level property rather
	// than a field; expose it the way Connect does.
	if it.Notes != "" {
		out.Fields = append(out.Fields, opitem.Field{
			ID:      "notesPlain",
			Label:   "notesPlain",
			Purpose: "NOTES",
			Value:   it.Notes,
		})
	}
	return out
}

// purposeForFieldID maps the SDK's well-known built-in field IDs onto the
// purpose labels the Connect API uses, keeping ${secret.x.username} and
// ${secret.x.password} working identically on both backends.
func purposeForFieldID(id string) string {
	switch id {
	case "username":
		return "USERNAME"
	case "password":
		return "PASSWORD"
	case "notesPlain":
		return "NOTES"
	default:
		return ""
	}
}
