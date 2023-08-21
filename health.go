package grace

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"golang.org/x/sync/errgroup"
)

// HealthChecker is something that needs its health checked to be "ready".
type HealthChecker interface {
	CheckHealth(ctx context.Context) error
}

// HealthCheckerFunc is a function that can be used as a HealthChecker.
type HealthCheckerFunc func(context.Context) error

// CheckHealth checks the health of a resource using the HealthCheckerFunc.
func (hcf HealthCheckerFunc) CheckHealth(ctx context.Context) error {
	return hcf(ctx)
}

// newHealthHandler returns an http.Handler capable of serving health checks.
//
// The handler returned is kubernetes aware, in that it serves a "liveness"
// endpoint under livenessEndpoint, and a "readiness" endpoint under readinessEndpoint.
func newHealthHandler(
	logger *slog.Logger,
	livenessEndpoint string,
	readinessEndpoint string,
	checkers ...HealthChecker,
) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc(livenessEndpoint, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		io.WriteString(rw, `{"healthy":true}`) //nolint: errcheck
	})

	mux.HandleFunc(readinessEndpoint, func(rw http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// We do not make a group with a shared context in order to avoid a single
		// check failing causing all of the other checks to erroneously fail and
		// lead to it being difficult to determine which actually failed.
		g := &errgroup.Group{}
		for _, checker := range checkers {
			checker := checker
			g.Go(func() error {
				err := checker.CheckHealth(ctx)
				if err != nil {
					logger.ErrorContext(ctx, "checking health",
						"error", err,
						"checker", fmt.Sprintf("%T", checker),
					)
				}
				return err
			})
		}

		if err := g.Wait(); err != nil {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusInternalServerError)
			io.WriteString(rw, `{"ready":false}`) //nolint: errcheck
			return
		}

		rw.Header().Set("Content-Type", "application/json")
		io.WriteString(rw, `{"ready":true}`) //nolint: errcheck
	})

	return mux
}
