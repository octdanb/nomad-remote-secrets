package awsssm

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// fakeAPI records the last GetParameter input and returns canned responses.
type fakeAPI struct {
	value   string
	err     error
	lastGet *ssm.GetParameterInput
}

func (f *fakeAPI) GetParameter(ctx context.Context, in *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	f.lastGet = in
	if f.err != nil {
		return nil, f.err
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: aws.String(f.value)}}, nil
}

func (f *fakeAPI) DescribeParameters(ctx context.Context, in *ssm.DescribeParametersInput, optFns ...func(*ssm.Options)) (*ssm.DescribeParametersOutput, error) {
	return &ssm.DescribeParametersOutput{}, f.err
}

func testProvider(f *fakeAPI, decrypt bool) *Provider {
	return newWithAPI(Config{Region: "us-east-1", Decrypt: decrypt}, f)
}

func TestResolveScalar(t *testing.T) {
	p := testProvider(&fakeAPI{value: "hunter2"}, true)
	res, err := p.Resolve(context.Background(), "aws-ssm:/prod/db/password")
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
	p := testProvider(&fakeAPI{value: `{"username":"app","password":"pw"}`}, true)
	res, err := p.Resolve(context.Background(), "aws-ssm:/prod/db/creds")
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
	p := testProvider(&fakeAPI{value: `{"username":"app"}`}, true)
	res, err := p.Resolve(context.Background(), "aws-ssm:/prod/db/creds?raw=true")
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

func TestResolveDecryptFalse(t *testing.T) {
	f := &fakeAPI{value: "v"}
	p := testProvider(f, true)
	if _, err := p.Resolve(context.Background(), "aws-ssm:/p?decrypt=false"); err != nil {
		t.Fatal(err)
	}
	if f.lastGet == nil || f.lastGet.WithDecryption == nil || *f.lastGet.WithDecryption {
		t.Errorf("WithDecryption not overridden to false: %+v", f.lastGet)
	}
	if aws.ToString(f.lastGet.Name) != "/p" {
		t.Errorf("Name = %q", aws.ToString(f.lastGet.Name))
	}
}

func TestResolveError(t *testing.T) {
	p := testProvider(&fakeAPI{err: errors.New("ParameterNotFound")}, true)
	if _, err := p.Resolve(context.Background(), "aws-ssm:/missing"); err == nil {
		t.Error("expected error to surface from GetParameter")
	}
}

func TestParseInvalid(t *testing.T) {
	p := testProvider(&fakeAPI{}, true)
	for _, ref := range []string{"op://x/y", "aws-ssm:", ""} {
		if _, _, err := p.CacheKey(ref); err == nil {
			t.Errorf("CacheKey(%q) = nil error, want error", ref)
		}
	}
}
