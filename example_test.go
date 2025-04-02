package grace_test

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/morningconsult/grace"
)

func Example_minimal() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	// Set up database pools, other application things, server handlers,
	// etc.
	// ....

	httpHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello there"))
	})

	// This is the absolute minimum configuration necessary to have a gracefully
	// shutdown server.
	g := grace.New(ctx, grace.WithServer("localhost:9090", httpHandler))
	if err := g.Run(ctx); err != nil {
		panic(err)
	}

	// Output:
}

func Example_minimal_with_healthcheck() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	// Set up database pools, other application things, server handlers,
	// etc.
	// ....

	httpHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello there"))
	})

	dbPinger := grace.HealthCheckerFunc(func(context.Context) error {
		// ping a database, etc.
		return nil
	})

	// This is the minimum configuration for a gracefully shutdown server
	// along with a health check server. This is most likely what you would
	// want to implement.
	g := grace.New(
		ctx,
		grace.WithHealthCheckServer(
			"localhost:9092",
			grace.WithCheckers(dbPinger),
		),
		grace.WithServer(
			"localhost:9090",
			httpHandler,
			grace.WithServerName("api"),
		),
	)

	if err := g.Run(ctx); err != nil {
		panic(err)
	}

	// Output:
}

func Example_full() {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Get addresses of dependencies, such as redis, postgres, etc
	// from CLI flags or other configuration. Wait for them to be available
	// before proceeding with setting up database connections and such.
	err := grace.Wait(ctx, 10*time.Second, grace.WithWaitForTCP("example.com:80"))
	if err != nil {
		panic(err)
	}

	// Set up database pools, other application things, server handlers,
	// etc.
	// ....

	httpHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello there"))
	})

	metricsHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("here are the metrics"))
	})

	dbPinger := grace.HealthCheckerFunc(func(context.Context) error {
		// ping database
		return nil
	})

	redisPinger := grace.HealthCheckerFunc(func(context.Context) error {
		// ping redis.
		return nil
	})

	bgWorker := func(context.Context) error {
		// Start some background work
		return nil
	}

	otherBackgroundWorker := func(context.Context) error {
		// Start some more background work
		return nil
	}

	// Create the new grace instance with your addresses/handlers.
	g := grace.New(
		ctx,
		grace.WithHealthCheckServer(
			"localhost:9092",
			grace.WithCheckers(dbPinger, redisPinger),
			grace.WithLivenessEndpoint("/-/live"),
			grace.WithReadinessEndpoint("/-/ready"),
		),
		grace.WithServer(
			"localhost:9090",
			httpHandler,
			grace.WithServerName("api"),
			grace.WithServerReadTimeout(grace.DefaultReadTimeout),
			grace.WithServerStopTimeout(10*time.Second),
			grace.WithServerWriteTimeout(grace.DefaultWriteTimeout),
		),
		grace.WithServer(
			"localhost:9091",
			metricsHandler,
			grace.WithServerName("metrics"),
			grace.WithServerStopTimeout(5*time.Second),
		),
		grace.WithBackgroundJobs(
			bgWorker,
			otherBackgroundWorker,
		),
		grace.WithStopSignals(
			os.Interrupt,
			syscall.SIGHUP,
			syscall.SIGTERM,
		),
	)

	if err = g.Run(ctx); err != nil {
		panic(err)
	}

	// Output:
}

func ExampleWait() {
	ctx := context.Background()

	es := &http.Server{
		Addr: "localhost:9200",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	}
	defer es.Shutdown(ctx) //nolint:errcheck

	go func() {
		if err := es.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	pg, err := net.Listen("tcp", "localhost:6379")
	if err != nil {
		panic(err)
	}
	defer pg.Close() //nolint:errcheck

	redis, err := net.Listen("tcp", "localhost:5432")
	if err != nil {
		panic(err)
	}
	defer redis.Close() //nolint:errcheck

	// Get addresses of dependencies, such as redis, postgres, etc
	// from CLI flags or other configuration. Wait for them to be available
	// before proceeding with setting up database connections and such.
	err = grace.Wait(
		ctx,
		50*time.Millisecond,
		grace.WithWaitForTCP("localhost:6379"),
		grace.WithWaitForTCP("localhost:5432"),
		grace.WithWaitForHTTP("http://localhost:9200"),
	)
	if err != nil {
		panic(err)
	}

	// Output:
}
