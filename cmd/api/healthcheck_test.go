package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kavi-sarna/fan-out-go-multistore-service/internal/httpapi"
)

// The address a test server is listening on, as the ADDR a container would
// pass.
func addrOf(t *testing.T, server *httptest.Server) string {
	t.Helper()
	return strings.TrimPrefix(server.URL, "http://")
}

func TestHealthcheckPassesAgainstTheRealRoutes(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(httpapi.Routes())
	defer server.Close()

	if err := healthcheck(t.Context(), addrOf(t, server)); err != nil {
		t.Fatalf("healthcheck against a healthy service: %v", err)
	}
}

// Anything but 200 is a failure. A container that reports healthy while
// answering 500 is worse than one that reports nothing.
func TestHealthcheckFailsOnANon200(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	err := healthcheck(t.Context(), addrOf(t, server))
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("healthcheck against a 503 returned %v, want an error naming the status", err)
	}
}

func TestHealthcheckFailsWhenNothingIsListening(t *testing.T) {
	t.Parallel()

	// Take a free port and release it, so nothing is there.
	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	if err := healthcheck(t.Context(), addr); err == nil {
		t.Fatal("healthcheck with nothing listening returned nil")
	}
}

// ":8080" is what a container is told so its port mapping can reach it. The
// probe has to turn that into an address it can actually dial.
func TestHealthcheckProbesLoopbackForAnUnspecifiedHost(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(httpapi.Routes())
	defer server.Close()
	_, port, err := net.SplitHostPort(addrOf(t, server))
	if err != nil {
		t.Fatalf("splitting %s: %v", server.URL, err)
	}

	for _, addr := range []string{":" + port, "0.0.0.0:" + port, "[::]:" + port} {
		if err := healthcheck(t.Context(), addr); err != nil {
			t.Errorf("healthcheck(%q): %v", addr, err)
		}
	}
}

// A service that accepts the connection and never answers must not hang the
// probe, or the orchestrator's check hangs with it.
func TestHealthcheckGivesUpOnAServiceThatNeverAnswers(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(t.Context(), healthcheckTimeout*3)
	defer cancel()

	if err := healthcheck(ctx, addrOf(t, server)); err == nil {
		t.Fatal("healthcheck against a hung service returned nil")
	}
}

func TestHealthcheckRejectsAnAddressWithoutAPort(t *testing.T) {
	t.Parallel()

	if err := healthcheck(t.Context(), "localhost"); err == nil {
		t.Fatal("healthcheck accepted an address with no port")
	}
}

// The healthcheck probes this process and nothing else. A host in ADDR that
// is neither loopback nor unspecified is refused before any request is made,
// so the environment cannot turn the probe into a client of somewhere else.
func TestHealthcheckRefusesToDialAnythingButLoopback(t *testing.T) {
	t.Parallel()

	// A server that fails the test if it is ever reached, standing in for
	// "somewhere else".
	reached := make(chan struct{}, 1)
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached <- struct{}{}
	}))
	defer elsewhere.Close()
	_, port, _ := net.SplitHostPort(addrOf(t, elsewhere))

	for _, host := range []string{"example.com", "10.0.0.5", "192.168.1.1", "203.0.113.7"} {
		err := healthcheck(t.Context(), net.JoinHostPort(host, port))
		if err == nil || !strings.Contains(err.Error(), "not loopback") {
			t.Errorf("healthcheck(%s:%s) = %v, want a refusal", host, port, err)
		}
	}
	select {
	case <-reached:
		t.Fatal("the healthcheck made a request to a non-loopback address")
	default:
	}
}

func TestHealthcheckRejectsAPortThatIsNotAPort(t *testing.T) {
	t.Parallel()

	for _, addr := range []string{":http", ":0", ":70000", ":-1"} {
		if err := healthcheck(t.Context(), addr); err == nil {
			t.Errorf("healthcheck(%q) accepted it", addr)
		}
	}
}
