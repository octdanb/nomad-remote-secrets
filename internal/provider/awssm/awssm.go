// Package awssm implements the aws-sm: secret provider backed by AWS Secrets
// Manager. A reference is the secret Name (or ARN) handed straight to
// GetSecretValue; Secrets Manager decrypts values server-side and JSON objects
// auto-expand to interpolation keys. Binary secrets (SecretBinary) are
// base64-encoded. It exposes the backend behind the provider.Provider
// interface so the plugin's fetch/check loop stays backend-neutral.
package awssm

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/octdanb/nomad-secret-plugin/internal/provider"
)

// Scheme is the reference scheme this provider handles.
const Scheme = "aws-sm"

// Config holds the Secrets Manager backend settings. Credentials are resolved
// by the AWS SDK's default chain (env, shared config, instance role); this
// plugin only pins the region/profile/endpoint. There is no Decrypt setting —
// Secrets Manager decrypts server-side.
type Config struct {
	Region      string        // AWS_REGION
	Profile     string        // AWS_PROFILE
	EndpointURL string        // AWS_ENDPOINT_URL (e.g. localstack for tests)
	Timeout     time.Duration // request timeout
}

// api is the slice of the Secrets Manager client this provider uses. Narrowing
// it to an interface lets unit tests inject a fake without a live AWS account.
type api interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
	ListSecrets(ctx context.Context, in *secretsmanager.ListSecretsInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error)
}

// Provider is the aws-sm: implementation of provider.Provider.
type Provider struct {
	cfg    Config
	client api
}

// New constructs an aws-sm: provider that builds its Secrets Manager client
// lazily on first use (see lazyClient), so an unreachable AWS still falls back
// to the stale cache instead of failing before the cache is consulted.
func New(cfg Config) *Provider {
	return &Provider{cfg: cfg, client: &lazyClient{cfg: cfg}}
}

// newWithAPI constructs a provider with an injected client, for tests.
func newWithAPI(cfg Config, client api) *Provider {
	return &Provider{cfg: cfg, client: client}
}

// Describe names the active backend for error messages and diagnostics; never
// secrets.
func (p *Provider) Describe() string {
	if p.cfg.EndpointURL != "" {
		return "AWS Secrets Manager at " + p.cfg.EndpointURL
	}
	return "AWS Secrets Manager (region " + p.cfg.Region + ")"
}

// options carries the per-reference query options parsed from "?k=v".
type options struct {
	secretID string // the Secrets Manager secret Name or ARN
	version  string // VersionId
	stage    string // VersionStage label (e.g. AWSPREVIOUS)
	raw      bool   // do not auto-expand JSON; expose only "value"
	binary   bool   // force treating the secret as binary
}

// parse strips the aws-sm: scheme and reads the option query. The remainder
// after the scheme is the SecretId passed straight to Secrets Manager (which
// accepts both names and ARNs), so it is not otherwise interpreted here.
func (p *Provider) parse(ref string) (options, error) {
	ref = strings.TrimSpace(ref)
	rest := strings.TrimPrefix(ref, Scheme+":")
	if rest == ref || rest == "" {
		return options{}, fmt.Errorf("invalid aws-sm reference %q: want aws-sm:<secret-name-or-arn>", ref)
	}

	opts := options{secretID: rest}
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		opts.secretID = rest[:i]
		q, err := url.ParseQuery(rest[i+1:])
		if err != nil {
			return options{}, fmt.Errorf("invalid options in %q: %w", ref, err)
		}
		opts.version = q.Get("version")
		opts.stage = q.Get("stage")
		if v := q.Get("raw"); v != "" {
			opts.raw = v == "true"
		}
		if v := q.Get("binary"); v != "" {
			opts.binary = v == "true"
		}
	}
	if opts.secretID == "" {
		return options{}, fmt.Errorf("invalid aws-sm reference %q: empty secret id", ref)
	}
	return opts, nil
}

// CacheKey returns the backend-namespaced cache key for ref. Secrets Manager
// has no OTP concept, so every reference is cacheable.
func (p *Provider) CacheKey(ref string) (string, bool, error) {
	opts, err := p.parse(ref)
	if err != nil {
		return "", false, err
	}
	scope := Scheme + "|" + p.cfg.Region + "|" + p.cfg.EndpointURL + "|" + opts.secretID + "|" + opts.version + "|" + opts.stage
	return scope, true, nil
}

// Resolve fetches the secret and returns its interpolation key/value map.
// Unless ?raw=true is set, a JSON-object SecretString auto-expands to per-key
// values. A SecretBinary secret is base64-encoded into "value_base64" (and
// "value" for safe interpolation).
func (p *Provider) Resolve(ctx context.Context, ref string) (provider.Result, error) {
	opts, err := p.parse(ref)
	if err != nil {
		return provider.Result{}, err
	}

	in := &secretsmanager.GetSecretValueInput{SecretId: aws.String(opts.secretID)}
	if opts.version != "" {
		in.VersionId = aws.String(opts.version)
	}
	if opts.stage != "" {
		in.VersionStage = aws.String(opts.stage)
	}

	out, err := p.client.GetSecretValue(ctx, in)
	if err != nil {
		return provider.Result{}, fmt.Errorf("resolving %s: %w", ref, err)
	}

	// A string secret (the common case) unless the caller forces binary.
	if out.SecretString != nil && !opts.binary {
		value := *out.SecretString
		if opts.raw {
			return provider.Result{Values: map[string]string{"value": value}, Object: false}, nil
		}
		values, obj := provider.ExpandJSON(value)
		return provider.Result{Values: values, Object: obj}, nil
	}

	// A binary secret: base64-encode the bytes. "value" mirrors the encoding
	// so ${secret.block.value} is always safe. (The richer file-secret pattern
	// is Phase 4.)
	if out.SecretBinary != nil {
		enc := base64.StdEncoding.EncodeToString(out.SecretBinary)
		return provider.Result{Values: map[string]string{"value": enc, "value_base64": enc}, Object: false}, nil
	}

	return provider.Result{}, fmt.Errorf("resolving %s: secret has neither SecretString nor SecretBinary", ref)
}

// Ping verifies credentials, region, and connectivity with a cheap
// ListSecrets call for the check command.
func (p *Provider) Ping(ctx context.Context) error {
	_, err := p.client.ListSecrets(ctx, &secretsmanager.ListSecretsInput{
		MaxResults: aws.Int32(1),
	})
	return err
}

// lazyClient builds the real Secrets Manager client on first use via the SDK's
// default config loader, honouring the pinned region/profile/endpoint.
// Deferring construction keeps the stale-cache fallback reachable when AWS is
// down.
type lazyClient struct {
	cfg    Config
	client *secretsmanager.Client
}

func (l *lazyClient) ensure(ctx context.Context) error {
	if l.client != nil {
		return nil
	}
	var loadOpts []func(*awsconfig.LoadOptions) error
	if l.cfg.Region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(l.cfg.Region))
	}
	if l.cfg.Profile != "" {
		loadOpts = append(loadOpts, awsconfig.WithSharedConfigProfile(l.cfg.Profile))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return fmt.Errorf("loading AWS config: %w", err)
	}
	l.client = secretsmanager.NewFromConfig(awsCfg, func(o *secretsmanager.Options) {
		if l.cfg.EndpointURL != "" {
			o.BaseEndpoint = aws.String(l.cfg.EndpointURL)
		}
	})
	return nil
}

func (l *lazyClient) GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	if err := l.ensure(ctx); err != nil {
		return nil, err
	}
	return l.client.GetSecretValue(ctx, in, optFns...)
}

func (l *lazyClient) ListSecrets(ctx context.Context, in *secretsmanager.ListSecretsInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error) {
	if err := l.ensure(ctx); err != nil {
		return nil, err
	}
	return l.client.ListSecrets(ctx, in, optFns...)
}
