package awssm

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// fakeAPI records the last GetSecretValue input and returns canned responses.
type fakeAPI struct {
	str     *string
	bin     []byte
	err     error
	lastGet *secretsmanager.GetSecretValueInput
}

func (f *fakeAPI) GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	f.lastGet = in
	if f.err != nil {
		return nil, f.err
	}
	return &secretsmanager.GetSecretValueOutput{SecretString: f.str, SecretBinary: f.bin}, nil
}

func (f *fakeAPI) ListSecrets(ctx context.Context, in *secretsmanager.ListSecretsInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error) {
	return &secretsmanager.ListSecretsOutput{}, f.err
}

func testProvider(f *fakeAPI) *Provider {
	return newWithAPI(Config{Region: "us-east-1"}, f)
}

func TestResolveScalar(t *testing.T) {
	p := testProvider(&fakeAPI{str: aws.String("hunter2")})
	res, err := p.Resolve(context.Background(), "aws-sm:prod/db/password")
	if err != nil {
		t.Fatal(err)
	}
	if res.Object {
		t.Error("Object = true, want false for a scalar")
	}
	if res.Values["value"] != "hunter2" {
		t.Errorf("value = %q", res.Values["value"])
	}
}

func TestResolveJSONAutoExpand(t *testing.T) {
	p := testProvider(&fakeAPI{str: aws.String(`{"username":"app","password":"pw"}`)})
	res, err := p.Resolve(context.Background(), "aws-sm:prod/db/creds")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Object {
		t.Error("Object = false, want true for a JSON object")
	}
	if res.Values["username"] != "app" || res.Values["password"] != "pw" {
		t.Errorf("not expanded: %v", res.Values)
	}
}

func TestResolveRaw(t *testing.T) {
	p := testProvider(&fakeAPI{str: aws.String(`{"username":"app"}`)})
	res, err := p.Resolve(context.Background(), "aws-sm:prod/db/creds?raw=true")
	if err != nil {
		t.Fatal(err)
	}
	if res.Object {
		t.Error("Object = true, want false with ?raw=true")
	}
	if _, ok := res.Values["username"]; ok {
		t.Errorf("raw should not expand keys: %v", res.Values)
	}
	if res.Values["value"] != `{"username":"app"}` {
		t.Errorf("value = %q", res.Values["value"])
	}
}

func TestResolveBinary(t *testing.T) {
	raw := []byte{0x00, 0x01, 0x02, 0xff}
	p := testProvider(&fakeAPI{bin: raw})
	res, err := p.Resolve(context.Background(), "aws-sm:certs/key")
	if err != nil {
		t.Fatal(err)
	}
	if res.Object {
		t.Error("Object = true, want false for a binary secret")
	}
	want := base64.StdEncoding.EncodeToString(raw)
	if res.Values["value_base64"] != want {
		t.Errorf("value_base64 = %q, want %q", res.Values["value_base64"], want)
	}
	if res.Values["value"] != want {
		t.Errorf("value = %q, want %q", res.Values["value"], want)
	}
}

func TestResolveBinaryForced(t *testing.T) {
	// Even with a SecretString present, ?binary=true forces base64 handling.
	p := testProvider(&fakeAPI{str: aws.String("ignored"), bin: []byte("hi")})
	res, err := p.Resolve(context.Background(), "aws-sm:s?binary=true")
	if err != nil {
		t.Fatal(err)
	}
	want := base64.StdEncoding.EncodeToString([]byte("hi"))
	if res.Values["value_base64"] != want {
		t.Errorf("value_base64 = %q, want %q", res.Values["value_base64"], want)
	}
}

func TestResolveVersionStage(t *testing.T) {
	f := &fakeAPI{str: aws.String("v")}
	p := testProvider(f)
	if _, err := p.Resolve(context.Background(), "aws-sm:s?version=abc&stage=AWSPREVIOUS"); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(f.lastGet.SecretId) != "s" {
		t.Errorf("SecretId = %q", aws.ToString(f.lastGet.SecretId))
	}
	if aws.ToString(f.lastGet.VersionId) != "abc" {
		t.Errorf("VersionId = %q", aws.ToString(f.lastGet.VersionId))
	}
	if aws.ToString(f.lastGet.VersionStage) != "AWSPREVIOUS" {
		t.Errorf("VersionStage = %q", aws.ToString(f.lastGet.VersionStage))
	}
}

func TestResolveError(t *testing.T) {
	p := testProvider(&fakeAPI{err: errors.New("ResourceNotFoundException")})
	if _, err := p.Resolve(context.Background(), "aws-sm:missing"); err == nil {
		t.Error("expected error to surface from GetSecretValue")
	}
}

func TestParseInvalid(t *testing.T) {
	p := testProvider(&fakeAPI{})
	for _, ref := range []string{"op://x/y", "aws-sm:", ""} {
		if _, _, err := p.CacheKey(ref); err == nil {
			t.Errorf("CacheKey(%q) = nil error, want error", ref)
		}
	}
}
