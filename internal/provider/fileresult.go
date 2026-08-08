package provider

import (
	"encoding/base64"
	"unicode/utf8"
)

// FileResult builds a Result for a file-like reference (a 1Password document
// or file field, an AWS binary secret). The raw bytes are always exposed at
// "value_base64" so binary content is safe to interpolate; "value" carries the
// text only when data is valid UTF-8, and "filename" is set when the backend
// knows the original name. A file reference is scalar-ish, so Object is false.
func FileResult(name string, data []byte) Result {
	values := map[string]string{
		"value_base64": base64.StdEncoding.EncodeToString(data),
	}
	if utf8.Valid(data) {
		values["value"] = string(data)
	}
	if name != "" {
		values["filename"] = name
	}
	return Result{Values: values, Object: false}
}
