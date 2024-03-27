package grace

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"regexp"
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
	ctx, cancel := context.WithTimeout(ctx, timeout)
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

// urlRegexp is used to remove any protocol or path that might be present
// when creating a tcp waiter.
var urlRegexp = regexp.MustCompile("^(https?://)?(?P<host>.+):(?P<port>[0-9]+)(.*)?")

// WithWaitForTCP makes a new TCP waiter that will ping an address and return
// once it is reachable.
func WithWaitForTCP(addr string) WaitOption {
	return func(cfg waitConfig) waitConfig {
		cfg.waiters = append(cfg.waiters, netWaiter{
			addr:    urlRegexp.ReplaceAllString(addr, "$host:$port"),
			logger:  cfg.logger,
			network: "tcp",
		})
		return cfg
	}
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
	for {
		if err := checkContextDone(ctx, w.logger, w.addr); err != nil {
			return err
		}

		d := net.Dialer{
			Timeout: 300 * time.Millisecond,
		}
		conn, _ := d.DialContext(ctx, w.network, w.addr)
		if conn != nil {
			w.logger.DebugContext(ctx, "established connection to address",
				"address", w.addr,
			)
			defer conn.Close() //nolint:errcheck
			return nil
		}
	}
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
	for {
		if err := checkContextDone(ctx, w.logger, w.url); err != nil {
			return err
		}

		res, _ := w.client.Get(w.url)
		if res == nil {
			continue
		}
		res.Body.Close()

		if res.StatusCode < http.StatusInternalServerError {
			w.logger.DebugContext(ctx, "established connection to address",
				"address", w.url,
			)
			return nil
		}
	}
}

// checkContextDone checks if the provided context is done, and returns
// an error if it is.
func checkContextDone(ctx context.Context, logger *slog.Logger, addr string) error {
	select {
	case <-ctx.Done():
		logger.DebugContext(ctx, "failed to establish connection to address",
			"address", addr,
		)
		return serrors.Errorf("timed out connecting to %q", addr)
	default:
		return nil
	}
}
