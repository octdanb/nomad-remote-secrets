// Package awsssm implements the aws-ssm: secret provider backed by AWS
// Systems Manager Parameter Store. A reference is the parameter Name (or ARN)
// handed straight to GetParameter; SecureString values are decrypted by
// default and JSON objects auto-expand to interpolation keys. It exposes the
// backend behind the provider.Provider interface so the plugin's fetch/check
// loop stays backend-neutral.
package awsssm

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/octdanb/nomad-remote-secrets/internal/provider"
)

// Scheme is the reference scheme this provider handles.
const Scheme = "aws-ssm"

// Config holds the Parameter Store backend settings. Credentials come from the
// AWS SDK's default chain (env, shared config, instance role) unless static
// keys are pinned here — the latter is what lets the plugin authenticate under
// Nomad's controlled environment (and avoids stalling on instance-metadata
// probes when no role is available).
type Config struct {
	Region          string        // AWS_REGION
	Profile         string        // AWS_PROFILE
	EndpointURL     string        // AWS_ENDPOINT_URL (e.g. localstack for tests)
	AccessKeyID     string        // AWS_ACCESS_KEY_ID (optional static creds)
	SecretAccessKey string        // AWS_SECRET_ACCESS_KEY
	SessionToken    string        // AWS_SESSION_TOKEN
	Decrypt         bool          // default true: WithDecryption for SecureString
	Timeout         time.Duration // request timeout
}

// api is the slice of the SSM client this provider uses. Narrowing it to an
// interface lets unit tests inject a fake without a live AWS account.
type api interface {
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
	DescribeParameters(ctx context.Context, in *ssm.DescribeParametersInput, optFns ...func(*ssm.Options)) (*ssm.DescribeParametersOutput, error)
}

// Provider is the aws-ssm: implementation of provider.Provider.
type Provider struct {
	cfg    Config
	client api
}

// New constructs an aws-ssm: provider that builds its SSM client lazily on
// first use (see lazyClient), so an unreachable AWS still falls back to the
// stale cache instead of failing before the cache is consulted.
func New(cfg Config) *Provider {
	return &Provider{cfg: cfg, client: &lazyClient{cfg: cfg}}
}

// newWithAPI constructs a provider with an injected SSM client, for tests.
func newWithAPI(cfg Config, client api) *Provider {
	return &Provider{cfg: cfg, client: client}
}

// Describe names the active backend for error messages and diagnostics; never
// secrets.
func (p *Provider) Describe() string {
	if p.cfg.EndpointURL != "" {
		return "AWS Parameter Store at " + p.cfg.EndpointURL
	}
	return "AWS Parameter Store (region " + p.cfg.Region + ")"
}

// options carries the per-reference query options parsed from "?k=v".
type options struct {
	name    string // the SSM parameter Name or ARN
	decrypt bool
	raw     bool // do not auto-expand JSON; expose only "value"
}

// parse strips the aws-ssm: scheme and reads the option query. The remainder
// after the scheme is the parameter Name passed straight to SSM (which
// accepts both names and ARNs), so it is not otherwise interpreted here.
func (p *Provider) parse(ref string) (options, error) {
	ref = strings.TrimSpace(ref)
	rest := strings.TrimPrefix(ref, Scheme+":")
	if rest == ref || rest == "" {
		return options{}, fmt.Errorf("invalid aws-ssm reference %q: want aws-ssm:<parameter-name-or-arn>", ref)
	}

	opts := options{name: rest, decrypt: p.cfg.Decrypt}
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		opts.name = rest[:i]
		q, err := url.ParseQuery(rest[i+1:])
		if err != nil {
			return options{}, fmt.Errorf("invalid options in %q: %w", ref, err)
		}
		if v := q.Get("decrypt"); v != "" {
			opts.decrypt = v != "false"
		}
		if v := q.Get("raw"); v != "" {
			opts.raw = v == "true"
		}
	}
	if opts.name == "" {
		return options{}, fmt.Errorf("invalid aws-ssm reference %q: empty parameter name", ref)
	}
	return opts, nil
}

// CacheKey returns the backend-namespaced cache key for ref. Every Parameter
// Store reference is cacheable.
func (p *Provider) CacheKey(ref string) (string, bool, error) {
	opts, err := p.parse(ref)
	if err != nil {
		return "", false, err
	}
	scope := Scheme + "|" + p.cfg.Region + "|" + p.cfg.EndpointURL + "|" + opts.name
	return scope, true, nil
}

// Resolve fetches the parameter and returns its interpolation key/value map.
// Unless ?raw=true is set, a JSON-object value auto-expands to per-key values.
func (p *Provider) Resolve(ctx context.Context, ref string) (provider.Result, error) {
	opts, err := p.parse(ref)
	if err != nil {
		return provider.Result{}, err
	}

	out, err := p.client.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(opts.name),
		WithDecryption: aws.Bool(opts.decrypt),
	})
	if err != nil {
		return provider.Result{}, fmt.Errorf("resolving %s: %w", ref, err)
	}
	if out.Parameter == nil || out.Parameter.Value == nil {
		return provider.Result{}, fmt.Errorf("resolving %s: parameter has no value", ref)
	}
	value := *out.Parameter.Value

	if opts.raw {
		return provider.Result{Values: map[string]string{"value": value}, Object: false}, nil
	}
	values, obj := provider.ExpandJSON(value)
	return provider.Result{Values: values, Object: obj}, nil
}

// Ping verifies credentials, region, and connectivity with a cheap
// DescribeParameters call for the check command.
func (p *Provider) Ping(ctx context.Context) error {
	_, err := p.client.DescribeParameters(ctx, &ssm.DescribeParametersInput{
		MaxResults: aws.Int32(1),
	})
	return err
}

// lazyClient builds the real SSM client on first use via the SDK's default
// config loader, honouring the pinned region/profile/endpoint. Deferring
// construction keeps the stale-cache fallback reachable when AWS is down.
type lazyClient struct {
	cfg    Config
	client *ssm.Client
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
	// Static credentials, when configured, pin the credential source so the
	// SDK never falls through to a (possibly absent) instance-metadata probe.
	if l.cfg.AccessKeyID != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(l.cfg.AccessKeyID, l.cfg.SecretAccessKey, l.cfg.SessionToken)))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return fmt.Errorf("loading AWS config: %w", err)
	}
	l.client = ssm.NewFromConfig(awsCfg, func(o *ssm.Options) {
		if l.cfg.EndpointURL != "" {
			o.BaseEndpoint = aws.String(l.cfg.EndpointURL)
		}
	})
	return nil
}

func (l *lazyClient) GetParameter(ctx context.Context, in *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	if err := l.ensure(ctx); err != nil {
		return nil, err
	}
	return l.client.GetParameter(ctx, in, optFns...)
}

func (l *lazyClient) DescribeParameters(ctx context.Context, in *ssm.DescribeParametersInput, optFns ...func(*ssm.Options)) (*ssm.DescribeParametersOutput, error) {
	if err := l.ensure(ctx); err != nil {
		return nil, err
	}
	return l.client.DescribeParameters(ctx, in, optFns...)
}
