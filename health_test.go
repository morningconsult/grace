package grace_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/morningconsult/grace"
)

func TestCheckerFunc_CheckHealth(t *testing.T) {
	t.Parallel()

	called := false
	wantErr := errors.New("foo")

	f := func(context.Context) error {
		called = true
		return wantErr
	}

	checker := grace.HealthCheckerFunc(f)
	err := checker.CheckHealth(context.Background())

	assert.True(t, called)
	assert.Equal(t, wantErr, err)
}

func Test_NewHealthHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		checkers   []grace.HealthChecker
		wantStatus int
	}{
		{
			name:       "no checkers",
			wantStatus: http.StatusOK,
		},
		{
			name: "one checker success",
			checkers: []grace.HealthChecker{
				mockChecker{nil},
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "one checker failure",
			checkers: []grace.HealthChecker{
				mockChecker{errors.New("oh no")},
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "multi checker success",
			checkers: []grace.HealthChecker{
				mockChecker{nil},
				mockChecker{nil},
				mockChecker{nil},
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "multi checker one failure",
			checkers: []grace.HealthChecker{
				mockChecker{nil},
				mockChecker{errors.New("oh no")},
				mockChecker{nil},
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		tt := tt

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			addr := newRandomAddr(t)
			ctx, cancel := context.WithCancel(context.Background())
			grc := grace.New(ctx, grace.WithHealthCheckServer(addr, grace.WithCheckers(tt.checkers...)))

			g, ctx := errgroup.WithContext(ctx)
			g.Go(func() error {
				return grc.Run(ctx)
			})

			err := grace.Wait(ctx, time.Second, grace.WithWaitForTCP(addr))
			require.NoError(t, err)

			t.Run("liveness", func(t *testing.T) {
				res, err := http.Get("http://" + addr + "/livez")
				require.NoError(t, err)
				defer res.Body.Close()
				assert.Equal(t, http.StatusOK, res.StatusCode)
			})

			t.Run("readiness", func(t *testing.T) {
				res, err := http.Get("http://" + addr + "/readyz")
				require.NoError(t, err)
				defer res.Body.Close()
				assert.Equal(t, tt.wantStatus, res.StatusCode)
			})

			cancel()
			require.NoError(t, g.Wait())
		})
	}
}

type mockChecker struct {
	error
}

func (m mockChecker) CheckHealth(context.Context) error {
	return m.error
}
