package grace

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/go-cleanhttp"
	"github.com/morningconsult/serrors"
	"golang.org/x/sync/errgroup"
)

// Waiter is something that waits for a thing to be "ready".
type Waiter interface {
	Wait(ctx context.Context) error
}

// WaiterFunc is a function that can be used as a Waiter.
type WaiterFunc func(context.Context) error

// Wait waits for a resource using the WaiterFunc.
func (w WaiterFunc) Wait(ctx context.Context) error {
	return w(ctx)
}

// Wait waits for all the provided checker pings to be successful until
// the specified timeout is exceeded. It will block until all of the pings are
// successful and return nil, or return an error if any checker is failing by
// the time the timeout elapses.
//
// Wait can be used to wait for dependent services like sidecar upstreams to
// be available before proceeding with other parts of an application startup.
func Wait(ctx context.Context, timeout time.Duration, opts ...WaitOption) error {
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, serrors.Errorf("failed to connect within %s", timeout))
	defer cancel()

	cfg := waitConfig{
		logger: slog.Default(),
	}

	for _, opt := range opts {
		cfg = opt(cfg)
	}

	g, ctx := errgroup.WithContext(ctx)
	for _, waiter := range cfg.waiters {
		waiter := waiter
		g.Go(func() error {
			return waiter.Wait(ctx)
		})
	}

	return serrors.WithStack(g.Wait())
}

// WaitOption is a configurable option for [Wait].
type WaitOption func(cfg waitConfig) waitConfig

type waitConfig struct {
	logger  *slog.Logger
	waiters []Waiter
}

// WithWaitLogger configures the logger to use when calling [Wait].
func WithWaitLogger(logger *slog.Logger) WaitOption {
	return func(cfg waitConfig) waitConfig {
		cfg.logger = logger

		for i, waiter := range cfg.waiters {
			switch waiter := waiter.(type) {
			case httpWaiter:
				waiter.logger = logger
				cfg.waiters[i] = waiter
			case netWaiter:
				waiter.logger = logger
				cfg.waiters[i] = waiter
			}
		}

		return cfg
	}
}

// WithWaiter adds a waiter for use with [Wait].
func WithWaiter(w Waiter) WaitOption {
	return func(cfg waitConfig) waitConfig {
		cfg.waiters = append(cfg.waiters, w)
		return cfg
	}
}

// WithWaiterFunc adds a waiter for use with [Wait].
func WithWaiterFunc(w WaiterFunc) WaitOption {
	return func(cfg waitConfig) waitConfig {
		cfg.waiters = append(cfg.waiters, w)
		return cfg
	}
}

// WithWaitForTCP makes a new TCP waiter that will ping an address and return
// once it is reachable.
//
// The addr can be a valid URL (as accepted by [net/url]) or a network address
// in "host:port" form. URLs that do not follow HTTP or HTTPS schemes must
// specify the port explicitly.
func WithWaitForTCP(addr string) WaitOption {
	return func(cfg waitConfig) waitConfig {
		cfg.waiters = append(cfg.waiters, netWaiter{
			addr:    cleanTCPAddr(addr),
			logger:  cfg.logger,
			network: "tcp",
		})
		return cfg
	}
}

func cleanTCPAddr(addr string) string {
	if hp, ok := hostPortFromURL(addr); ok {
		return hp
	}

	return addr
}

func hostPortFromURL(addr string) (string, bool) {
	u, err := url.Parse(addr)
	if err != nil {
		return "", false
	}

	if u.Hostname() == "" {
		return "", false
	}

	port := u.Port()
	if u.Port() == "" {
		switch u.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return "", false
		}
	}

	return net.JoinHostPort(u.Hostname(), port), true
}

// WithWaitForUnix makes a new unix waiter that will ping a socket and return
// once it is reachable. The socketPath must be a valid filepath to the unix
// socket to connect with.
func WithWaitForUnix(socketPath string) WaitOption {
	return func(cfg waitConfig) waitConfig {
		cfg.waiters = append(cfg.waiters, netWaiter{
			addr:    socketPath,
			logger:  cfg.logger,
			network: "unix",
		})
		return cfg
	}
}

type netWaiter struct {
	addr    string
	logger  *slog.Logger
	network string
}

// Wait waits for something to be listening on the given network and address.
func (w netWaiter) Wait(ctx context.Context) error {
	d := net.Dialer{
		Timeout: 300 * time.Millisecond,
	}

	if err := waitAndRetry(ctx, func(ctx context.Context) waitResult {
		conn, err := d.DialContext(ctx, w.network, w.addr)
		if err != nil {
			w.logger.DebugContext(ctx, "failed to connect to address",
				"address", w.addr,
				"network", w.network,
				"error", err,
			)
			return waitResult{Ok: false}
		}
		defer conn.Close() //nolint:errcheck

		w.logger.DebugContext(ctx, "established connection to address",
			"address", w.addr,
			"network", w.network,
		)
		return waitResult{Ok: true}
	}); err != nil {
		return serrors.Errorf("connecting to %q: %w", w.addr, err)
	}

	return nil
}

// WithWaitForHTTP makes a new HTTP waiter that will make GET requests to a URL
// until it returns a non-500 error code. All statuses below 500 mean the dependency
// is accepting requests, even if the check is unauthorized or invalid.
func WithWaitForHTTP(url string) WaitOption {
	return func(cfg waitConfig) waitConfig {
		cfg.waiters = append(cfg.waiters,
			httpWaiter{
				client: cleanhttp.DefaultClient(),
				logger: cfg.logger,
				url:    url,
			},
		)
		return cfg
	}
}

// WithWaitForUnixHTTP makes a new HTTP waiter that will make GET requests to a unix
// domain socket and URL path until it returns a non-500 error code. All statuses
// below 500 mean the dependency is accepting requests, even if the check is unauthorized
// or invalid.
func WithWaitForUnixHTTP(socketPath, urlPath string) WaitOption {
	return func(cfg waitConfig) waitConfig {
		dialer := net.Dialer{
			Timeout: 300 * time.Millisecond,
		}

		transport := cleanhttp.DefaultTransport()
		transport.DialContext = func(ctx context.Context, _ string, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		}

		url := "http://unix/" + strings.TrimPrefix(urlPath, "/")

		cfg.waiters = append(cfg.waiters,
			httpWaiter{
				client: &http.Client{
					Transport: transport,
				},
				logger: cfg.logger,
				url:    url,
			},
		)
		return cfg
	}
}

type httpWaiter struct {
	client *http.Client
	logger *slog.Logger
	url    string
}

// Wait waits for something to be accepting HTTP requests.
func (w httpWaiter) Wait(ctx context.Context) error {
	if err := waitAndRetry(ctx, w.waitOnce); err != nil {
		return serrors.Errorf("connecting to %q: %w", w.url, err)
	}

	return nil
}

func (w httpWaiter) waitOnce(ctx context.Context) waitResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.url, nil)
	if err != nil {
		return waitResult{FatalErr: serrors.WithStack(err)}
	}

	res, err := w.client.Do(req)
	if err != nil {
		w.logger.DebugContext(ctx, "failed to connect to address",
			"address", w.url,
			"error", err,
		)
		return waitResult{Ok: false}
	}
	res.Body.Close()

	w.logger.DebugContext(ctx, "received status from address",
		"address", w.url,
		"http_status", res.StatusCode,
	)
	return waitResult{
		Ok: res.StatusCode < http.StatusInternalServerError,
	}
}

type waitResult struct {
	// Ok returns whether the operation was successful.
	Ok bool
	// FatalErr indicates the retry loops should end immediately.
	FatalErr error
}

func waitAndRetry(
	ctx context.Context,
	waitFunc func(context.Context) waitResult,
) error {
	// In an ideal world, this is configurable. Without making any breaking
	// changes, this is probably a reasonable interval compared to the previous
	// behavior of "make a new request as soon as the old one finished".
	interval := 100 * time.Millisecond

	var tick <-chan time.Time
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	result := make(chan waitResult, 1)

	go func() { result <- waitFunc(ctx) }()

	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case got := <-result:
			switch {
			case got.FatalErr != nil:
				return got.FatalErr
			case got.Ok:
				return nil
			default:
				tick = ticker.C
			}
		case <-tick:
			tick = nil
			go func() { result <- waitFunc(ctx) }()
		}
	}
}
