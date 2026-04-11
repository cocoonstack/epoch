package server

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

type testCtxKey struct{}

func TestNewHTTPServerConfiguresTimeoutsAndBaseContext(t *testing.T) {
	ctx := context.WithValue(t.Context(), testCtxKey{}, "value")

	srv := newHTTPServer(ctx, ":8080", http.NewServeMux())

	if got, want := srv.ReadHeaderTimeout, 5*time.Second; got != want {
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

func TestServeOnListenerShutsDownOnContextCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	started := make(chan struct{})
	canceled := make(chan struct{})
	srv := newHTTPServer(ctx, ln.Addr().String(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-r.Context().Done()
		close(canceled)
	}))

	done := make(chan error, 1)
	go func() {
		done <- serveOnListener(ctx, srv, ln)
	}()

	clientDone := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ln.Addr().String(), nil)
		if err != nil {
			clientDone <- err
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		clientDone <- err
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not start")
	}

	cancel()

	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("handler context was not canceled")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveOnListener returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveOnListener did not return after cancel")
	}

	select {
	case <-clientDone:
	case <-time.After(5 * time.Second):
		t.Fatal("client request did not finish")
	}
}
