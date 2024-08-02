package grace_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morningconsult/grace"
)

func Test_Wait(t *testing.T) {
	t.Parallel()

	t.Run("success tcp", func(t *testing.T) {
		t.Parallel()

		addr1 := newTestAddr(t)
		addr2 := newTestAddr(t)

		ctx := context.Background()

		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		}))

		err := grace.Wait(
			ctx,
			50*time.Millisecond,
			grace.WithWaitForTCP(addr1),
			grace.WithWaitForTCP(addr2),
			grace.WithWaiter(grace.WaiterFunc(func(context.Context) error {
				return nil
			})),
			grace.WithWaiterFunc(func(context.Context) error {
				return nil
			}),
			grace.WithWaitLogger(logger),
		)
		require.NoError(t, err)

		assert.NotZero(t, buf)
	})

	t.Run("success tcp from url", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name string
			url  string
		}{
			{
				name: "https with path",
				url:  "https://" + newTestAddr(t) + "/foo/bar",
			},
			{
				name: "https without path",
				url:  "https://" + newTestAddr(t),
			},
			{
				name: "http with path",
				url:  "http://" + newTestAddr(t) + "/foo/bar",
			},
			{
				name: "http without path",
				url:  "http://" + newTestAddr(t),
			},
			{
				name: "host port",
				url:  newTestAddr(t),
			},
		}

		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				ctx := context.Background()
				err := grace.Wait(ctx, 50*time.Millisecond, grace.WithWaitForTCP(tt.url))
				assert.NoError(t, err)
			})
		}
	})

	t.Run("success http", func(t *testing.T) {
		t.Parallel()

		addr1 := "http://" + newTestAddr(t)
		addr2 := "http://" + newTestAddr(t)

		var wasCalled bool
		t.Cleanup(func() { assert.True(t, wasCalled, "wanted handler called") })
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			wasCalled = true
			// This ensures that statuses < 500 are still considered online.
			w.WriteHeader(http.StatusUnauthorized)
		}))
		t.Cleanup(ts.Close)

		ctx := context.Background()

		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		}))

		err := grace.Wait(
			ctx,
			50*time.Millisecond,
			grace.WithWaitForHTTP(addr1),
			grace.WithWaitForHTTP(addr2),
			grace.WithWaitForHTTP(ts.URL),
			grace.WithWaitLogger(logger),
		)
		require.NoError(t, err)

		assert.NotZero(t, buf)
	})

	t.Run("timeout tcp", func(t *testing.T) {
		t.Parallel()

		addr1 := newTestAddr(t)
		addr2 := newRandomAddr(t)

		ctx := context.Background()
		err := grace.Wait(
			ctx,
			50*time.Millisecond,
			grace.WithWaitForTCP(addr1),
			grace.WithWaitForTCP(addr2),
		)
		require.Error(t, err)

		wantError := fmt.Sprintf("connecting to %q: failed to connect within 50ms", addr1)
		if strings.Contains(err.Error(), addr2) {
			wantError = fmt.Sprintf("connecting to %q: failed to connect within 50ms", addr2)
		}
		require.EqualError(t, err, wantError)
	})

	t.Run("timeout http", func(t *testing.T) {
		t.Parallel()

		addr1 := "http://" + newTestAddr(t)
		addr2 := "http://" + newRandomAddr(t)

		var wasCalled bool
		t.Cleanup(func() {
			assert.True(t, wasCalled, "wanted http server called")
		})
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			wasCalled = true
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(ts.Close)
		addr3 := ts.URL

		ctx := context.Background()
		err := grace.Wait(
			ctx,
			50*time.Millisecond,
			grace.WithWaitForHTTP(addr1),
			grace.WithWaitForHTTP(addr2),
			grace.WithWaitForHTTP(addr3),
		)
		require.Error(t, err)

		var wantAddr string
		switch {
		case strings.Contains(err.Error(), addr1):
			wantAddr = addr1
		case strings.Contains(err.Error(), addr2):
			wantAddr = addr2
		case strings.Contains(err.Error(), addr3):
			wantAddr = addr3
		}
		require.EqualError(t, err, fmt.Sprintf("connecting to %q: failed to connect within 50ms", wantAddr))
	})

	t.Run("success unix", func(t *testing.T) {
		t.Parallel()

		socket := newTestUnixServer(t, http.NotFoundHandler())

		ctx := context.Background()
		err := grace.Wait(
			ctx,
			50*time.Millisecond,
			grace.WithWaitForUnix(socket),
		)
		require.NoError(t, err)
	})

	t.Run("timeout unix", func(t *testing.T) {
		t.Parallel()

		socket := filepath.Join(t.TempDir(), "server.sock")

		ctx := context.Background()
		err := grace.Wait(
			ctx,
			50*time.Millisecond,
			grace.WithWaitForUnix(socket),
		)
		require.EqualError(t, err, fmt.Sprintf("connecting to %q: failed to connect within 50ms", socket))
	})

	t.Run("success unix http", func(t *testing.T) {
		t.Parallel()

		var gotRequest bool
		t.Cleanup(func() {
			assert.True(t, gotRequest, "wanted unix server to receive request")
		})

		socket := newTestUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotRequest = true
			assert.Equal(t, "/foo/bar", r.URL.Path)
			assert.Empty(t, r.URL.Host)

			w.WriteHeader(http.StatusOK)
		}))

		ctx := context.Background()
		err := grace.Wait(
			ctx,
			50*time.Millisecond,
			grace.WithWaitForUnixHTTP(socket, "/foo/bar"),
		)
		require.NoError(t, err)
	})

	t.Run("timeout unix http", func(t *testing.T) {
		t.Parallel()

		var gotRequest bool
		t.Cleanup(func() {
			assert.True(t, gotRequest, "wanted unix server to receive request")
		})

		socket := newTestUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotRequest = true
			assert.Equal(t, "/foo/bar", r.URL.Path)
			assert.Empty(t, r.URL.Host)

			w.WriteHeader(http.StatusNotImplemented)
		}))

		ctx := context.Background()
		err := grace.Wait(
			ctx,
			50*time.Millisecond,
			grace.WithWaitForUnixHTTP(socket, "/foo/bar"),
		)
		require.EqualError(t, err, `connecting to "http://unix/foo/bar": failed to connect within 50ms`)
	})
}

// newTestUnixServer makes a new unix server with the provided handler, returning
// the unix socket address.
func newTestUnixServer(t *testing.T, handler http.Handler) string {
	t.Helper()

	socket := filepath.Join(t.TempDir(), "server.sock")

	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)

	ts := httptest.NewUnstartedServer(handler)
	ts.Listener = listener
	ts.Start()
	t.Cleanup(ts.Close)

	return socket
}
