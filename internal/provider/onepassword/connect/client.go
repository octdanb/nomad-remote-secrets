// Package connect is a minimal client for the 1Password Connect REST API
// (https://developer.1password.com/docs/connect/connect-api-reference/).
// It implements only what the plugin needs: resolving vaults and items by
// name or ID and reading item fields. It deliberately has no dependencies
// outside the standard library so the plugin stays a small, auditable
// static binary.
package connect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/octdanb/nomad-remote-secrets/internal/provider/onepassword/opitem"
)

// idPattern matches 1Password object IDs (26 lowercase base32 characters).
var idPattern = regexp.MustCompile(`^[a-z0-9]{26}$`)

// Client talks to a 1Password Connect server.
type Client struct {
	host  string
	token string
	http  *http.Client
}

// New returns a client for the Connect server at host (e.g.
// "http://localhost:8080") authenticating with the given bearer token.
func New(host, token string, timeout time.Duration) *Client {
	return &Client{
		host:  strings.TrimRight(host, "/"),
		token: token,
		http:  &http.Client{Timeout: timeout},
	}
}

// Vault is a subset of the Connect API vault object.
type Vault struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Section is a named group of fields within an item.
type Section struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Field is a subset of the Connect API item field object.
type Field struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Type    string   `json:"type"`    // e.g. STRING, CONCEALED, FILE
	Purpose string   `json:"purpose"` // e.g. USERNAME, PASSWORD, NOTES
	Value   string   `json:"value"`
	Section *Section `json:"section"`
}

// File is a subset of the Connect API item file object. FILE-type fields
// reference one of these by ID; a Document item carries one in its files list.
type File struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Size        int      `json:"size"`
	ContentPath string   `json:"content_path"`
	FieldID     string   `json:"field_id"`
	Section     *Section `json:"section"`
}

// Item is a subset of the Connect API item object.
type Item struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Category string    `json:"category"`
	Fields   []Field   `json:"fields"`
	Sections []Section `json:"sections"`
	Files    []File    `json:"files"`
}

// apiError is the error body Connect returns on non-2xx responses.
type apiError struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
}

// ListVaults returns every vault visible to the Connect token.
func (c *Client) ListVaults(ctx context.Context) ([]opitem.Vault, error) {
	var vaults []Vault
	if err := c.get(ctx, "/v1/vaults", nil, &vaults); err != nil {
		return nil, fmt.Errorf("listing vaults: %w", err)
	}
	out := make([]opitem.Vault, 0, len(vaults))
	for _, v := range vaults {
		out = append(out, opitem.Vault{ID: v.ID, Name: v.Name})
	}
	return out, nil
}

// GetVault resolves a vault by ID or, failing that, by exact name.
func (c *Client) GetVault(ctx context.Context, nameOrID string) (*opitem.Vault, error) {
	if idPattern.MatchString(nameOrID) {
		var v Vault
		if err := c.get(ctx, "/v1/vaults/"+nameOrID, nil, &v); err == nil {
			return &opitem.Vault{ID: v.ID, Name: v.Name}, nil
		}
		// An ID-shaped string can legitimately be a vault name; fall
		// through to a name lookup before giving up.
	}

	var vaults []Vault
	q := url.Values{"filter": {fmt.Sprintf("name eq %q", nameOrID)}}
	if err := c.get(ctx, "/v1/vaults", q, &vaults); err != nil {
		return nil, fmt.Errorf("listing vaults: %w", err)
	}
	switch len(vaults) {
	case 0:
		return nil, fmt.Errorf("no vault named %q is visible to this Connect token", nameOrID)
	case 1:
		return &opitem.Vault{ID: vaults[0].ID, Name: vaults[0].Name}, nil
	default:
		return nil, fmt.Errorf("%d vaults named %q; reference the vault by ID instead", len(vaults), nameOrID)
	}
}

// GetItem resolves an item within a vault by ID or exact title and returns it
// with its full field values.
func (c *Client) GetItem(ctx context.Context, vaultID, nameOrID string) (*opitem.Item, error) {
	if idPattern.MatchString(nameOrID) {
		var it Item
		if err := c.get(ctx, "/v1/vaults/"+vaultID+"/items/"+nameOrID, nil, &it); err == nil {
			return toOpItem(&it), nil
		}
	}

	var summaries []Item
	q := url.Values{"filter": {fmt.Sprintf("title eq %q", nameOrID)}}
	if err := c.get(ctx, "/v1/vaults/"+vaultID+"/items", q, &summaries); err != nil {
		return nil, fmt.Errorf("listing items: %w", err)
	}
	switch len(summaries) {
	case 0:
		return nil, fmt.Errorf("no item titled %q in vault", nameOrID)
	case 1:
		// The list endpoint returns summaries without field values;
		// fetch the full item.
		var it Item
		if err := c.get(ctx, "/v1/vaults/"+vaultID+"/items/"+summaries[0].ID, nil, &it); err != nil {
			return nil, err
		}
		return toOpItem(&it), nil
	default:
		return nil, fmt.Errorf("%d items titled %q in vault; reference the item by ID instead", len(summaries), nameOrID)
	}
}

// toOpItem converts the Connect wire format into the backend-neutral model.
func toOpItem(it *Item) *opitem.Item {
	out := &opitem.Item{
		ID:       it.ID,
		Title:    it.Title,
		Category: it.Category,
	}
	for _, s := range it.Sections {
		out.Sections = append(out.Sections, opitem.Section{ID: s.ID, Label: s.Label})
	}
	// Index files by the field they back so FILE-type fields can carry a
	// FileID for lazy content fetching.
	filesByField := map[string]File{}
	for _, fl := range it.Files {
		if fl.FieldID != "" {
			filesByField[fl.FieldID] = fl
		}
		of := opitem.File{
			ID:          fl.ID,
			Name:        fl.Name,
			Size:        fl.Size,
			FieldID:     fl.FieldID,
			ContentPath: fl.ContentPath,
		}
		if fl.Section != nil {
			of.SectionID = fl.Section.ID
		}
		out.Files = append(out.Files, of)
	}
	for _, f := range it.Fields {
		nf := opitem.Field{
			ID:      f.ID,
			Label:   f.Label,
			Type:    f.Type,
			Purpose: f.Purpose,
			Value:   f.Value,
		}
		if f.Section != nil {
			nf.SectionID = f.Section.ID
		}
		if fl, ok := filesByField[f.ID]; ok {
			nf.FileID = fl.ID
		}
		out.Fields = append(out.Fields, nf)
	}
	return out
}

// GetFileContent fetches the raw bytes of a file attached to an item. The
// Connect API serves file content at
// /v1/vaults/{v}/items/{i}/files/{f}/content.
func (c *Client) GetFileContent(ctx context.Context, vaultID, itemID, fileID string) ([]byte, error) {
	path := "/v1/vaults/" + vaultID + "/items/" + itemID + "/files/" + fileID + "/content"
	data, err := c.getRaw(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("reading file content: %w", err)
	}
	return data, nil
}

// getRaw performs an authenticated GET and returns the raw response body,
// used for binary file content that must not be JSON-decoded.
func (c *Client) getRaw(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.host+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connecting to 1Password Connect at %s: %w", c.host, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var ae apiError
		if json.Unmarshal(body, &ae) == nil && ae.Message != "" {
			return nil, fmt.Errorf("1Password Connect: %s (HTTP %d)", ae.Message, resp.StatusCode)
		}
		return nil, fmt.Errorf("1Password Connect: HTTP %d from %s", resp.StatusCode, path)
	}
	return body, nil
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	u := c.host + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to 1Password Connect at %s: %w", c.host, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var ae apiError
		if json.Unmarshal(body, &ae) == nil && ae.Message != "" {
			return fmt.Errorf("1Password Connect: %s (HTTP %d)", ae.Message, resp.StatusCode)
		}
		return fmt.Errorf("1Password Connect: HTTP %d from %s", resp.StatusCode, path)
	}
	return json.Unmarshal(body, out)
}
