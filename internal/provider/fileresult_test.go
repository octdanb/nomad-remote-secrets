package provider

import (
	"encoding/base64"
	"testing"
)

func TestFileResultUTF8(t *testing.T) {
	data := []byte("hello, cert\n")
	r := FileResult("bundle.pem", data)

	if r.Object {
		t.Error("file result must not be an object")
	}
	if got := r.Values["value"]; got != string(data) {
		t.Errorf("value = %q, want %q", got, data)
	}
	if got, want := r.Values["value_base64"], base64.StdEncoding.EncodeToString(data); got != want {
		t.Errorf("value_base64 = %q, want %q", got, want)
	}
	if got := r.Values["filename"]; got != "bundle.pem" {
		t.Errorf("filename = %q, want bundle.pem", got)
	}
}

func TestFileResultBinary(t *testing.T) {
	data := []byte{0xff, 0xfe, 0x00, 0x01} // not valid UTF-8
	r := FileResult("keystore.p12", data)

	if _, ok := r.Values["value"]; ok {
		t.Error("binary content must not set value")
	}
	if got, want := r.Values["value_base64"], base64.StdEncoding.EncodeToString(data); got != want {
		t.Errorf("value_base64 = %q, want %q", got, want)
	}
	if got := r.Values["filename"]; got != "keystore.p12" {
		t.Errorf("filename = %q, want keystore.p12", got)
	}
}

func TestFileResultNoName(t *testing.T) {
	r := FileResult("", []byte("x"))
	if _, ok := r.Values["filename"]; ok {
		t.Error("filename must be omitted when unknown")
	}
}

func TestFileResultEmpty(t *testing.T) {
	// An empty file is valid UTF-8, so "value" is present but empty and
	// "value_base64" is the empty string.
	r := FileResult("empty.txt", []byte{})
	if got, ok := r.Values["value"]; !ok || got != "" {
		t.Errorf("value = %q (present=%v), want empty and present", got, ok)
	}
	if got := r.Values["value_base64"]; got != "" {
		t.Errorf("value_base64 = %q, want empty", got)
	}
	if got := r.Values["filename"]; got != "empty.txt" {
		t.Errorf("filename = %q, want empty.txt", got)
	}
}
