package provider

import (
	"context"
	"testing"
)

// stubProvider is a no-op Provider for routing tests.
type stubProvider struct{ name string }

func (s stubProvider) CacheKey(string) (string, bool, error) { return s.name, true, nil }
func (s stubProvider) Resolve(context.Context, string) (Result, error) {
	return Result{}, nil
}
func (s stubProvider) Ping(context.Context) error { return nil }
func (s stubProvider) Describe() string           { return s.name }

func TestRouteByScheme(t *testing.T) {
	reg := NewRegistry()
	reg.Register("op", stubProvider{name: "op"})
	reg.Register("aws-ssm", stubProvider{name: "ssm"})

	for ref, want := range map[string]string{
		"op://Prod/db/pw":  "op",
		"aws-ssm:/prod/db": "ssm",
	} {
		p, err := reg.Route(ref)
		if err != nil {
			t.Fatalf("Route(%q) error: %v", ref, err)
		}
		if p.Describe() != want {
			t.Errorf("Route(%q) = %q, want %q", ref, p.Describe(), want)
		}
	}

	if _, err := reg.Route("vault://x/y"); err == nil {
		t.Error("Route with unknown scheme: want error")
	}
	// Scheme-less is ambiguous when more than one provider is registered.
	if _, err := reg.Route("Prod/db/pw"); err == nil {
		t.Error("Route scheme-less with multiple providers: want error")
	}
}

func TestRouteSchemelessFallsBackToSoleProvider(t *testing.T) {
	reg := NewRegistry()
	reg.Register("op", stubProvider{name: "op"})

	p, err := reg.Route("Prod/db/pw")
	if err != nil {
		t.Fatalf("Route(scheme-less) with one provider: %v", err)
	}
	if p.Describe() != "op" {
		t.Errorf("got %q, want op", p.Describe())
	}
}
