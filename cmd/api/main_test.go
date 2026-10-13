package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

// quiet is a logger that goes nowhere: these tests assert on behavior, not
// on log output.
func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// listen opens a listener on a port the OS picks, so tests never collide.
func listen(t *testing.T) net.Listener {
	t.Helper()
	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	return listener
}

// TestServeDrainsRequestsAlreadyInFlight is the reason serve() exists as its
// own function.
//
// A request that has already started must be allowed to finish after SIGTERM.
// Without Shutdown this test fails with a connection reset: the process exits
// and the client is left holding half a response. That is what a deploy looks
// like to whoever was mid-request.
func TestServeDrainsRequestsAlreadyInFlight(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	const work = 300 * time.Millisecond

	mux := http.NewServeMux()
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		close(started)
		time.Sleep(work)
		fmt.Fprint(w, "finished")
	})

	listener := listen(t)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: readHeaderTimeout}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- serve(ctx, quiet(), server, listener, 5*time.Second) }()

	url := "http://" + listener.Addr().String()
	body := make(chan string, 1)
	go func() {
		// context.Background(), deliberately: canceling the server's ctx
		// must not also cancel the client, or the test would pass by
		// abandoning the request rather than by the server draining it.
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url+"/slow", nil)
		if err != nil {
			body <- "building request: " + err.Error()
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			body <- "request failed: " + err.Error()
			return
		}
		defer resp.Body.Close()
		read, err := io.ReadAll(resp.Body)
		if err != nil {
			body <- "read failed: " + err.Error()
			return
		}
		body <- string(read)
	}()

	// Shut down only once the handler is provably running, so this is about
	// draining rather than about whether the request arrived in time.
	<-started
	cancel()

	select {
	case got := <-body:
		if got != "finished" {
			t.Fatalf("in-flight request got %q, want %q", got, "finished")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request never completed")
	}

	if err := <-done; err != nil {
		t.Fatalf("serve returned %v", err)
	}
}

// After shutdown the listener is closed, so a new connection is refused
// rather than accepted and then dropped.
func TestServeStopsAcceptingAfterShutdown(t *testing.T) {
	t.Parallel()

	listener := listen(t)
	addr := listener.Addr().String()
	server := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: readHeaderTimeout}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- serve(ctx, quiet(), server, listener, time.Second) }()

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve returned %v", err)
	}

	dialCtx, dialCancel := context.WithTimeout(context.Background(), time.Second)
	defer dialCancel()
	if conn, err := new(net.Dialer).DialContext(dialCtx, "tcp", addr); err == nil {
		_ = conn.Close()
		t.Fatal("connected after shutdown; the listener is still open")
	}
}

// A handler that outlasts the grace period must not hold the process open.
// Shutdown reports the deadline, and serve passes that up rather than
// swallowing it.
func TestServeGivesUpOnHandlersThatOutlastTheGrace(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /stuck", func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	})

	listener := listen(t)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: readHeaderTimeout}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- serve(ctx, quiet(), server, listener, 100*time.Millisecond) }()

	go func() {
		req, err := http.NewRequestWithContext(
			context.Background(), http.MethodGet, "http://"+listener.Addr().String()+"/stuck", nil)
		if err != nil {
			return
		}
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()

	<-started
	cancel()

	err := <-done
	if err == nil {
		t.Fatal("serve returned nil; a stuck handler should surface the drain deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("serve returned %v, want a wrapped context.DeadlineExceeded", err)
	}
}

// A port already in use must fail at startup with a legible error, not inside
// a goroutine once the process looks healthy.
func TestRunReportsAPortAlreadyInUse(t *testing.T) {
	t.Parallel()

	taken := listen(t)
	defer taken.Close()

	err := run(context.Background(), quiet(), taken.Addr().String())
	if err == nil {
		t.Fatal("run returned nil for an address already in use")
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("run returned %v, want a net.OpError", err)
	}
}
