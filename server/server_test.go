package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	commonhttpx "github.com/cocoonstack/cocoon-common/httpx"
)

type testCtxKey struct{}

func TestNewHTTPServerConfiguresTimeoutsAndBaseContext(t *testing.T) {
	ctx := context.WithValue(t.Context(), testCtxKey{}, "value")

	srv := newHTTPServer(ctx, ":8080", http.NewServeMux())

	if got, want := srv.ReadHeaderTimeout, commonhttpx.DefaultReadHeaderTimeout; got != want {
		t.Fatalf("ReadHeaderTimeout = %v, want %v", got, want)
	}
	if got, want := srv.IdleTimeout, 60*time.Second; got != want {
		t.Fatalf("IdleTimeout = %v, want %v", got, want)
	}
	if srv.BaseContext == nil {
		t.Fatalf("BaseContext is nil")
	}
	if got := srv.BaseContext(nil); got != ctx {
		t.Fatalf("BaseContext() = %v, want %v", got, ctx)
	}
}
