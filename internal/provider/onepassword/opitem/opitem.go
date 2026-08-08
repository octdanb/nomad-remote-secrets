// Package opitem defines the backend-neutral model for 1Password objects.
// Both secret sources — the Connect REST client and the service-account SDK
// backend — resolve references into these types, so the plugin's field
// lookup and key-mapping logic is written once.
package opitem

// Vault identifies a 1Password vault.
type Vault struct {
	ID   string
	Name string
}

// Section is a named group of fields within an item.
type Section struct {
	ID    string
	Label string
}

// Field is a single item field.
type Field struct {
	ID        string
	Label     string
	Type      string // e.g. STRING, CONCEALED, OTP
	Purpose   string // e.g. USERNAME, PASSWORD, NOTES (empty if none)
	Value     string
	TOTP      string // current code, set on OTP fields when available
	SectionID string // empty for top-level fields
	FileID    string // set for FILE-type fields: the attached file's ID
}

// File is a file attached to an item — either a file field's attachment or,
// for a Document item, the item's document. Byte content is fetched lazily
// via the Source, never eagerly loaded with the item.
type File struct {
	ID          string
	Name        string
	Size        int
	SectionID   string // empty for a document / top-level attachment
	FieldID     string // set when the file backs a FILE-type field
	ContentPath string // Connect content URL, when the backend provides one
}

// Item is a 1Password item with its resolved field values.
type Item struct {
	ID       string
	Title    string
	Category string
	Fields   []Field
	Sections []Section
	Files    []File // file attachments and, for Document items, the document
}
