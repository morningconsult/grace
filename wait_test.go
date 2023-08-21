package grace_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
			grace.WithWaiter(grace.WaiterFunc(func(_ context.Context) error {
				return nil
			})),
			grace.WithWaiterFunc(func(_ context.Context) error {
				return nil
			}),
			grace.WithWaitLogger(logger),
		)
		require.NoError(t, err)

		assert.NotZero(t, buf)
	})

	t.Run("success tcp from url", func(t *testing.T) {
		t.Parallel()

		urls := []string{
			"https://" + newTestAddr(t) + "/foo/bar",
			newTestAddr(t) + "/foo/bar",
			"https://" + newTestAddr(t),
		}
		for _, url := range urls {
			ctx := context.Background()
			err := grace.Wait(ctx, 50*time.Millisecond, grace.WithWaitForTCP(url))
			assert.NoError(t, err)
		}
	})

	t.Run("success http", func(t *testing.T) {
		t.Parallel()

		addr1 := "http://" + newTestAddr(t)
		addr2 := "http://" + newTestAddr(t)

		var wasCalled bool
		t.Cleanup(func() { assert.True(t, wasCalled, "wanted handler called") })
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

		wantError := fmt.Sprintf("timed out connecting to %q", addr1)
		if strings.Contains(err.Error(), addr2) {
			wantError = fmt.Sprintf("timed out connecting to %q", addr2)
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
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		require.EqualError(t, err, fmt.Sprintf("timed out connecting to %q", wantAddr))
	})
}
