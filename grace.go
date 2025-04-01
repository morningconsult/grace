// Package grace starts and stops applications gracefully.
package grace

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/morningconsult/serrors"
	"golang.org/x/sync/errgroup"
)

const (
	// DefaultReadTimeout is the default timeout for reading http requests for
	// a server.
	DefaultReadTimeout = 1 * time.Minute

	// DefaultWriteTimeout is the default timeout for writing http responses for
	// a server.
	DefaultWriteTimeout = 5 * time.Minute

	// DefaultStopTimeout is the default timeout for stopping a server after a
	// signal is encountered.
	DefaultStopTimeout = 10 * time.Second

	// DefaultLivenessEndpoint is the default liveness endpoint for the health server.
	DefaultLivenessEndpoint = "/livez"

	// DefaultReadinessEndpoint is the default readiness endpoint for the health server.
	DefaultReadinessEndpoint = "/readyz"
)

// Grace handles graceful shutdown of http servers.
//
// Each of the servers specified will be started in order, with graceful
// handling of OS signals to allow any in-flight requests to complete
// before stopping them entirely.
//
// Additionally, a health check server will be started to receive health
// requests from external orchestration systems to confirm the aliveness
// of the application if added using [WithHealthCheckServer].
type Grace struct {
	health         healthConfig
	backgroundJobs []BackgroundJobFunc
	logger         *slog.Logger
	servers        []graceServer
	stopSignals    []os.Signal
}

// config configures a new [Grace].
type config struct {
	BackgroundJobs []BackgroundJobFunc
	Health         healthConfig
	Logger         *slog.Logger
	Servers        []serverConfig
	StopSignals    []os.Signal
}

// An Option is used to modify the grace config.
type Option func(cfg config) config

// WithStopSignals sets the stop signals to listen for.
//
// StopSignals are the signals to listen for to gracefully stop servers when
// encountered. If not specified, it defaults to [os.Interrupt],
// [syscall.SIGHUP], and [syscall.SIGTERM].
func WithStopSignals(signals ...os.Signal) Option {
	return func(cfg config) config {
		cfg.StopSignals = signals
		return cfg
	}
}

// WithLogger configures the logger to use.
func WithLogger(logger *slog.Logger) Option {
	return func(cfg config) config {
		cfg.Logger = logger

		for i := range cfg.Servers {
			cfg.Servers[i].Logger = logger
		}

		return cfg
	}
}

// healthConfig configures the [config.Health] of grace.
type healthConfig struct {
	Addr              string
	Checkers          []HealthChecker
	LivenessEndpoint  string
	ReadinessEndpoint string
}

// A HealthOption is is used to modify the grace health check server config.
type HealthOption func(cfg healthConfig) healthConfig

// WithHealthCheckServer adds a health check server to be run on the provided
// address in the form "ip:port" or "host:port". The checkers are the health
// checking functions to run for each request to the health check server.
func WithHealthCheckServer(addr string, opts ...HealthOption) Option {
	return func(cfg config) config {
		health := healthConfig{
			Addr:              addr,
			LivenessEndpoint:  DefaultLivenessEndpoint,
			ReadinessEndpoint: DefaultReadinessEndpoint,
		}

		for _, op := range opts {
			health = op(health)
		}

		cfg.Health = health
		return cfg
	}
}

// WithCheckers sets the [HealthChecker] functions to the health server will run.
func WithCheckers(checkers ...HealthChecker) HealthOption {
	return func(cfg healthConfig) healthConfig {
		cfg.Checkers = checkers
		return cfg
	}
}

// WithLivenessEndpoint sets the liveness endpoint for the health check server.
// If not used, it will default to [DefaultLivenessEndpoint].
func WithLivenessEndpoint(endpoint string) HealthOption {
	return func(cfg healthConfig) healthConfig {
		cfg.LivenessEndpoint = endpoint
		return cfg
	}
}

// WithReadinessEndpoint sets the liveness endpoint for the health check server.
// If not used, it will default to [DefaultReadinessEndpoint].
func WithReadinessEndpoint(endpoint string) HealthOption {
	return func(cfg healthConfig) healthConfig {
		cfg.ReadinessEndpoint = endpoint
		return cfg
	}
}

// BackgroundJobFunc is a function to invoke with the context returned from
// [signal.NotifyContext]. This can be used to ensure that non-http servers
// in the application, such as background workers, can also be tied into the
// signal context.
//
// The function will be called within a [golang.org/x/sync/errgroup.Group] and
// must be blocking.
type BackgroundJobFunc func(ctx context.Context) error

// WithBackgroundJobs sets the [BackgroundJobFunc] functions that will be
// invoked when [Run] is called.
func WithBackgroundJobs(jobs ...BackgroundJobFunc) Option {
	return func(cfg config) config {
		cfg.BackgroundJobs = jobs
		return cfg
	}
}

// serverConfig is the configuration for a single server.
type serverConfig struct {
	Addr         string
	Handler      http.Handler
	Logger       *slog.Logger
	Name         string
	ReadTimeout  time.Duration
	StopTimeout  time.Duration
	WriteTimeout time.Duration
}

// A ServerOption is used to modify a server config.
type ServerOption func(cfg serverConfig) serverConfig

// WithServer adds a new server to be handled by grace with the provided address
// and [http.Handler]. The address of the server to listen on
// should be in the form 'ip:port' or 'host:port'.
//
// The server's [http.Server.BaseContext] will be set to the context used when [New]
// is invoked.
func WithServer(addr string, handler http.Handler, options ...ServerOption) Option {
	return func(cfg config) config {
		srv := serverConfig{
			Addr:         addr,
			Handler:      handler,
			Logger:       cfg.Logger,
			ReadTimeout:  DefaultReadTimeout,
			StopTimeout:  DefaultStopTimeout,
			WriteTimeout: DefaultWriteTimeout,
		}
		for _, fn := range options {
			srv = fn(srv)
		}

		cfg.Servers = append(cfg.Servers, srv)
		return cfg
	}
}

// WithServerName sets the name of the server, which is a helpful name for the server
// for logging purposes.
func WithServerName(name string) ServerOption {
	return func(cfg serverConfig) serverConfig {
		cfg.Name = name
		return cfg
	}
}

// WithServerStopTimeout sets the stop timeout for the server.
//
// The StopTimeout is the amount of time to wait for the server to exit
// before forcing a shutdown. This determines the period that the
// "graceful" shutdown will last.
//
// If not used, the StopTimeout defaults to [DefaultStopTimeout].
// A timeout of 0 will result in the server being shut down immediately.
func WithServerStopTimeout(timeout time.Duration) ServerOption {
	return func(cfg serverConfig) serverConfig {
		cfg.StopTimeout = timeout
		return cfg
	}
}

// WithServerReadTimeout sets the read timeout for the server.
//
// ReadTimeout is the [http.Server.ReadTimeout] for the server.
// If not used, the ReadTimeout defaults to [DefaultReadTimeout].
func WithServerReadTimeout(timeout time.Duration) ServerOption {
	return func(cfg serverConfig) serverConfig {
		cfg.ReadTimeout = timeout
		return cfg
	}
}

// WithServerWriteTimeout sets the read timeout for the server.
//
// WriteTimeout is the [http.Server.WriteTimeout] for the server.
// If not used, the WriteTimeout defaults to [DefaultWriteTimeout].
func WithServerWriteTimeout(timeout time.Duration) ServerOption {
	return func(cfg serverConfig) serverConfig {
		cfg.WriteTimeout = timeout
		return cfg
	}
}

// New creates a new grace. Specify one or more [Option] to configure the new
// grace client.
//
// The provided context will be used as the base context for all created servers.
//
// New does not start listening for OS signals, it only creates the new grace that
// can be started by calling [Grace.Run].
func New(ctx context.Context, options ...Option) Grace {
	cfg := config{
		Logger:      slog.Default(),
		StopSignals: []os.Signal{os.Interrupt, syscall.SIGHUP, syscall.SIGTERM},
	}

	for _, op := range options {
		cfg = op(cfg)
	}

	srvs := make([]graceServer, 0, len(cfg.Servers))
	for _, srv := range cfg.Servers {
		srvs = append(srvs, newGraceServer(ctx, srv))
	}

	return Grace{
		backgroundJobs: cfg.BackgroundJobs,
		health:         cfg.Health,
		logger:         cfg.Logger,
		servers:        srvs,
		stopSignals:    cfg.StopSignals,
	}
}

// Run starts all of the registered servers and creates a new health check server,
// if configured with [WithHealthCheckServer].
//
// The created health check server will not be gracefully shutdown and will
// instead be stopped as soon as any stop signals are encountered or
// the context is finished. This is to ensure that any health checks to the
// application begin to fail immediately.
//
// They will all be stopped gracefully when the configured stop signals
// are encountered or the provided context is finished.
func (g Grace) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, g.stopSignals...)
	defer stop()

	if g.health.Addr != "" {
		g.servers = append(g.servers, newGraceServer(ctx, serverConfig{
			Addr: g.health.Addr,
			Handler: newHealthHandler(
				g.logger,
				g.health.LivenessEndpoint,
				g.health.ReadinessEndpoint,
				g.health.Checkers...,
			),
			Logger:       g.logger,
			Name:         "health",
			ReadTimeout:  DefaultReadTimeout,
			WriteTimeout: DefaultWriteTimeout,
		}))
	}

	return g.listenAndServe(ctx)
}

// listenAndServe starts the given servers with graceful shutdown handling for
// interrupts.
func (g Grace) listenAndServe(ctx context.Context) error {
	eg, ctx := errgroup.WithContext(ctx)

	for _, server := range g.servers {
		server := server
		eg.Go(func() error {
			return server.start(ctx)
		})

		eg.Go(func() error {
			// We need to block on the context being done otherwise we would be
			// shutting down immediately. The context will be done when the parent
			// context passed to listenAndServe gets canceled, or from the first
			// error returned from any other goroutine in the group.
			<-ctx.Done()
			return server.stop(ctx)
		})
	}

	for _, job := range g.backgroundJobs {
		job := job
		eg.Go(func() error {
			return job(ctx)
		})
	}

	return eg.Wait()
}

// newGraceServer creates a new [graceServer] from the provided configuration.
func newGraceServer(ctx context.Context, cfg serverConfig) graceServer {
	return graceServer{
		HTTPServer: &http.Server{
			BaseContext:  func(net.Listener) context.Context { return ctx },
			Addr:         cfg.Addr,
			Handler:      cfg.Handler,
			ReadTimeout:  cfg.ReadTimeout,
			WriteTimeout: cfg.WriteTimeout,
		},
		Logger:      cfg.Logger,
		Name:        cfg.Name,
		StopTimeout: cfg.StopTimeout,
	}
}

// graceServer is a single http graceServer with its human-readable name
// and stop timeout.
type graceServer struct {
	HTTPServer  *http.Server
	Logger      *slog.Logger
	Name        string
	StopTimeout time.Duration
}

// start starts the graceServer.
func (gs graceServer) start(ctx context.Context) error {
	gs.Logger.InfoContext(ctx, "server listening",
		"server", gs.Name,
		"address", gs.HTTPServer.Addr,
	)
	if err := gs.HTTPServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return serrors.WithStack(err)
	}
	return nil
}

// stop gracefully shuts down a grace HTTP server.
func (gs graceServer) stop(ctx context.Context) error {
	// We detach the passed context here because it could be canceled already,
	// which would defeat the purpose of gracefully draining.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gs.StopTimeout)
	defer cancel()

	gs.Logger.InfoContext(ctx, "server shutting down",
		"server", gs.Name,
		"address", gs.HTTPServer.Addr,
	)
	defer gs.Logger.InfoContext(ctx, "server shut down",
		"server", gs.Name,
		"address", gs.HTTPServer.Addr,
	)

	gs.HTTPServer.SetKeepAlivesEnabled(false)
	err := gs.HTTPServer.Shutdown(ctx)
	if errors.Is(err, context.DeadlineExceeded) && gs.StopTimeout == 0 {
		// If the server has a StopTimeout of 0, it always ends up with
		// its context deadline exceeded. Since this was explicitly set to
		// 0 by the user, we ignore this as an error.
		return nil
	}
	return serrors.WithStack(err)
}
