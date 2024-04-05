package grace_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/morningconsult/grace"
)

func TestGrace_Run(t *testing.T) {
	t.Parallel()

	t.Run("error address in use", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		addr := newTestAddr(t)
		handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
		grc := grace.New(
			ctx,
			grace.WithServer(
				addr,
				handler,
				grace.WithServerReadTimeout(time.Millisecond),
				grace.WithServerWriteTimeout(time.Microsecond),
				grace.WithServerStopTimeout(time.Millisecond),
			),
			grace.WithServer(addr, handler),
			grace.WithStopSignals(os.Kill),
		)

		err := grc.Run(ctx)
		require.Error(t, err, "wanted start error from grace")

		wantError := fmt.Sprintf("listen tcp %s: listen: address already in use", addr)
		if strings.Contains(err.Error(), "bind") {
			wantError = fmt.Sprintf("listen tcp %s: bind: address already in use", addr)
		}
		require.EqualError(t, err, wantError)
	})

	t.Run("error background job", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		grc := grace.New(
			ctx,
			grace.WithBackgroundJobs(func(context.Context) error {
				return errors.New("wombat")
			}),
		)

		err := grc.Run(ctx)
		require.EqualError(t, err, "wombat")
	})

	t.Run("success shutdown context done", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		addr1 := newRandomAddr(t)
		addr2 := newRandomAddr(t)
		healthAddr := newRandomAddr(t)
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		})

		var checkerWasCalled bool
		t.Cleanup(func() {
			assert.True(t, checkerWasCalled, "wanted checker to be called")
		})

		grc := grace.New(
			ctx,
			grace.WithHealthCheckServer(
				healthAddr,
				grace.WithCheckers(grace.HealthCheckerFunc(func(context.Context) error {
					checkerWasCalled = true
					return nil
				})),
				grace.WithLivenessEndpoint("/foo"),
				grace.WithReadinessEndpoint("/bar"),
			),
			grace.WithServer(
				addr1,
				handler,
				grace.WithServerName("test1"),
				grace.WithServerStopTimeout(200*time.Millisecond),
			),
			grace.WithServer(
				addr2,
				handler,
				grace.WithServerName("test2"),
				grace.WithServerStopTimeout(200*time.Millisecond),
			),
		)

		ctx, cancel := context.WithCancel(ctx)
		g := &errgroup.Group{}
		g.Go(func() error {
			return grc.Run(ctx)
		})

		err := grace.Wait(
			ctx,
			3*time.Second,
			grace.WithWaitForTCP(addr1),
			grace.WithWaitForTCP(addr2),
			grace.WithWaitForTCP(healthAddr),
		)
		require.NoError(t, err)

		// This is to test the server is fully online.
		for _, addr := range []string{addr1, addr2} {
			res, err := http.Get("http://" + addr)
			require.NoError(t, err)
			assert.Equal(t, http.StatusTeapot, res.StatusCode)
		}

		// And the health check server is online.
		for _, path := range []string{"/foo", "/bar"} {
			res, err := http.Get("http://" + healthAddr + path)
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, res.StatusCode)
		}

		cancel()
		require.NoError(t, g.Wait())
	})

	t.Run("error shutdown deadline exceeded", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		addr1 := newRandomAddr(t)
		addr2 := newRandomAddr(t)
		healthAddr := newRandomAddr(t)

		reqCh := make(chan struct{})
		handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			reqCh <- struct{}{}
			time.Sleep(10 * time.Second)
		})

		grc := grace.New(
			ctx,
			grace.WithHealthCheckServer(healthAddr),
			grace.WithServer(
				addr1,
				handler,
				grace.WithServerName("test1"),
				grace.WithServerStopTimeout(0),
			),
			grace.WithServer(
				addr2,
				handler,
				grace.WithServerName("test2"),
				grace.WithServerStopTimeout(100*time.Millisecond),
			),
		)

		ctx, cancel := context.WithCancel(ctx)
		g := &errgroup.Group{}
		g.Go(func() error {
			return grc.Run(ctx)
		})

		err := grace.Wait(
			ctx,
			time.Second,
			grace.WithWaitForTCP(addr1),
			grace.WithWaitForTCP(addr2),
			grace.WithWaitForTCP(healthAddr),
		)
		require.NoError(t, err)

		go func() {
			http.Get("http://" + addr2) //nolint:errcheck
		}()

		<-reqCh
		cancel()
		require.EqualError(t, g.Wait(), context.DeadlineExceeded.Error())
	})

	t.Run("WithLogger", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, nil))

		ctx := context.Background()
		addr := newTestAddr(t)
		handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
		grc := grace.New(
			ctx,
			grace.WithServer(addr, handler),
			grace.WithLogger(logger),
		)

		err := grc.Run(ctx)
		require.Error(t, err, "wanted start error from grace")

		assert.NotZero(t, buf)
	})
}

// newTestAddr starts a new httptest server and returns it's address.
// The test server will have a noop handler for all requests.
func newTestAddr(t *testing.T) string {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(ts.Close)
	return strings.TrimPrefix(ts.URL, "http://")
}

// newRandomAddr is used to get an open port for test servers to prevent tests
// from failing due to a static port being in use.
func newRandomAddr(t *testing.T) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := strings.TrimPrefix(ts.URL, "http://")
	ts.Close()
	return addr
}
