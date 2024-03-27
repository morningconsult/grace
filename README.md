# grace

[![Go Reference](https://pkg.go.dev/badge/github.com/morningconsult/grace.svg)](https://pkg.go.dev/github.com/morningconsult/grace)

A Go library for starting and stopping applications gracefully.

Grace facilitates gracefully starting and stopping a Go web application.
It helps with waiting for dependencies - such as sidecar upstreams - to be available
and handling operating system signals to shut down.

Requires Go >= 1.21.

## Usage

In your project directory:

```shell
go get github.com/morningconsult/grace
```

## Features

* Graceful handling of upstream dependencies that might not be available when
  your application starts
* Graceful shutdown of multiple HTTP servers when operating system signals are
  received, allowing in-flight requests to finish.
* Automatic startup and control of a dedicated health check HTTP server.
* Passing of signal context to other non-HTTP components with a generic
  function signature.

### Gracefully shutting down an application

Many HTTP applications need to handle graceful shutdowns so that in-flight requests
are not terminated, leaving an unsatisfactory experience for the requester. Grace
helps with this by catching operating system signals and allowing your HTTP servers
to finish processing requests before being forcefully stopped.

To use this, add something similar to the following example to the end of your
application's entrypoint. `grace.Run` should be returned in your entrypoint/main
function.

An absolute minimal configuration to get a graceful server would be the following:

```go
ctx := context.Background()
httpHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
  w.Write([]byte("hello there"))
})

// This is the absolute minimum configuration necessary to have a gracefully
// shutdown server.
g := grace.New(ctx, grace.WithServer("localhost:9090", httpHandler))
err := g.Run(ctx)
```

Additionally, it will also handle setting up a health check server with any check functions
necessary. The health server will be shut down as soon as a signal is caught. This
helps to ensure that the orchestration system running your application marks it as unhealthy
and stops sending it any new requests, while the in-flight requests to your actual
application are still allowed to finish gracefully.

An minimal example with a health check server and your application server would be similar
to the following:

```go
httpHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
  w.Write([]byte("hello there"))
})

dbPinger := grace.HealthCheckerFunc(func(ctx context.Context) error {
  // ping database
  return nil
})

g := grace.New(
  ctx,
  grace.WithHealthCheckServer("localhost:9092", grace.WithCheckers(dbPinger)),
  grace.WithServer("localhost:9090", httpHandler, grace.WithServerName("api")),
)
```

A full example with multiple servers, background jobs, and health checks:

```go
// Set up database pools, other application things, server handlers,
// etc.
httpHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
  w.Write([]byte("hello there"))
})

metricsHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
  w.Write([]byte("here are the metrics"))
})

dbPinger := grace.HealthCheckerFunc(func(ctx context.Context) error {
  // ping database
  return nil
})

redisPinger := grace.HealthCheckerFunc(func(ctx context.Context) error {
  // ping redis.
  return nil
})

bgWorker := func(ctx context.Context) error {
  // Start some background work
  return nil
}

// Create the new grace instance with your addresses/handlers.
// Here, we create:
//
//  1. A health check server listening on 0.0.0.0:9092 that will
//     respond to requests at /-/live and /-/ready, running the dbPinger
//     and redisPinger functions for each request to /-/ready.
//     This overrides the default endpoints of /livez and /readyz.
//  2. Our application server on localhost:9090 with the httpHandler.
//     It specifies the default read and write timeouts, and a graceful
//     stop timeout of 10 seconds.
//  3. Our metrics server on localhost:9091, with a shorter stop timeout
//     of 5 seconds.
//  4. A function to start a background worker process that will be called
//     with the context to be notified from OS signals, allowing for background
//     processes to also get stopped when a signal is received.
//  5. A custom list of operating system signals to intercept that override the
//     defaults.
g := grace.New(
  ctx,
  grace.WithHealthCheckServer(
    "0.0.0.0:9092",
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
  grace.WithBackgroundJobs(bgWorker),
  grace.WithStopSignals(
    os.Interrupt,
    syscall.SIGHUP,
    syscall.SIGTERM,
  ),
)

if err = g.Run(ctx); err != nil {
  log.Fatal(err)
}
```

### Waiting for dependencies

If your application has upstream dependencies, such as a sidecar that exposes a
remote database, you can use grace to wait for them to be available before
attempting a connection.

At the top of your application's entrypoint (before setting up database connections!)
use the `Wait` method to wait for specific addresses to respond to TCP/HTTP pings before
continuing with your application setup:

```go
err := grace.Wait(
  ctx,
  10*time.Second,
  grace.WithWaitForTCP("localhost:6379"), // redis
  grace.WithWaitForTCP("localhost:5432"), // postgres
  grace.WithWaitForUnix("/tmp/something.sock"), // something on a unix socket
  grace.WithWaitForHTTP("http://localhost:9200"), // elasticsearch
  grace.WithWaitForHTTP("http://localhost:19000/ready"), // envoy sidecar
  grace.WithWaitForUnixHTTP("/tmp/envoy.sock", "/ready"), // HTTP over unix socket
)
if err != nil {
	log.Fatal(err)
}
```

## Local Development

### Testing

#### Linting

The project uses [`golangci-lint`](https://golangci-lint.run) for linting. Run
with

```sh
golangci-lint run
```

Configuration is found in:

- `./.golangci.yaml` - Linter configuration.

#### Unit Tests

Run unit tests with

```sh
go test ./...
```
