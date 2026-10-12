package client

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/guygrigsby/sock-contract/gen/go/sock/v1/sockv1connect"
)

// UnaryTimeout bounds each attempt of a unary call; Subscribe has no deadline.
const UnaryTimeout = 30 * time.Second

// Clients are the two sock services.
type Clients struct {
	Identity  sockv1connect.IdentityServiceClient
	Messaging sockv1connect.MessagingServiceClient
}

// New builds clients that send cfg.Token and userAgent on every request to
// the configured host, and only there. No client-wide timeout: Subscribe
// streams indefinitely. Redirects are never followed.
func New(cfg Config, userAgent string) Clients {
	base, _ := url.Parse(cfg.URL) // Validate checked the URL; a nil-safe zero URL matches nothing
	if base == nil {
		base = &url.URL{}
	}
	hc := &http.Client{
		Transport: &transport{
			base:   http.DefaultTransport,
			token:  cfg.Token,
			ua:     userAgent,
			scheme: base.Scheme,
			host:   base.Host,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return Clients{
		Identity:  sockv1connect.NewIdentityServiceClient(hc, cfg.URL),
		Messaging: sockv1connect.NewMessagingServiceClient(hc, cfg.URL),
	}
}

type transport struct {
	base         http.RoundTripper
	token, ua    string
	scheme, host string
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if r.URL.Scheme == t.scheme && r.URL.Host == t.host {
		r.Header.Set("Authorization", "Bearer "+t.token)
	}
	r.Header.Set("User-Agent", t.ua)
	return t.base.RoundTrip(r)
}

// Call runs one unary RPC under r with a deadline per attempt. The error is
// already actionable text (see Describe).
func Call[T any](ctx context.Context, r Retrier, fn func(context.Context) (T, error)) (T, error) {
	var out T
	err := r.Do(ctx, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, UnaryTimeout)
		defer cancel()
		var err error
		out, err = fn(ctx)
		return err
	})
	return out, Describe(err)
}
