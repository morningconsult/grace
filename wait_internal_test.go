package grace

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_cleanTCPAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"example.com:123", "example.com:123"},
		{"foobar:123", "foobar:123"},
		{"198.51.100.0:123", "198.51.100.0:123"},
		{"http://example.com:123", "example.com:123"},
		{"https://example.com:123", "example.com:123"},
		{"http://example.com", "example.com:80"},
		{"https://example.com", "example.com:443"},
		{"http://example.com/", "example.com:80"},
		{"http://example.com/foo", "example.com:80"},
		{"http://example.com:123/foo", "example.com:123"},
		{"http://user:password@example.com:123/foo", "example.com:123"},
		{"postgres://example.com:5432/foo", "example.com:5432"},
		// This won't work, but we aren't going to hard-code every possible URL
		// scheme to be able to support port-less URLs natively.
		{"postgres://example.com/foo", "postgres://example.com/foo"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()

			got := cleanTCPAddr(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_httpWaiter_waitOnce_nil_context(t *testing.T) {
	var w httpWaiter

	res := w.waitOnce(nil) //nolint:staticcheck // nil context is intentional
	assert.False(t, res.Ok)
	assert.EqualError(t, res.FatalErr, "net/http: nil Context")
}

func Test_waitAndRetry(t *testing.T) {
	ctx := context.Background()

	err := waitAndRetry(ctx, func(context.Context) waitResult {
		return waitResult{FatalErr: errors.New("oh no")}
	})
	assert.EqualError(t, err, "oh no")
}
