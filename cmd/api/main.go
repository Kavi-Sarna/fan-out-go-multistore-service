// Command api is the service entry point: read configuration, build the
// server, run it until signaled, and drain.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/kavi-sarna/fan-out-go-multistore-service/internal/httpapi"
)

const (
	defaultAddr = ":8080"

	// How long a handler already in flight has to finish once shutdown has
	// begun. Past this the process exits and those requests are lost, which
	// is the lesser evil: a deploy that waits forever on one stuck handler is
	// an outage dressed as politeness.
	defaultGrace = 15 * time.Second

	// Slowloris: a client that opens a connection and dribbles headers holds
	// a goroutine indefinitely. Go's zero value for this is no timeout at all.
	//
	// ReadTimeout, WriteTimeout and IdleTimeout are deliberately not set yet.
	// Their right values depend on what the handlers do, and the handlers do
	// nothing. They are due with the stores, in PR 7.
	readHeaderTimeout = 5 * time.Second

	// Shorter than any orchestrator's probe interval, so a hung service fails
	// its check rather than wedging the thing that is checking it.
	healthcheckTimeout = 2 * time.Second

	// The subcommand the Dockerfile's HEALTHCHECK runs. Named once so a test
	// can hold the two together.
	healthcheckCommand = "healthcheck"
)

func main() {
	// The runtime image is distroless: no shell, no curl, no wget. A
	// container healthcheck can only run a binary that is already there, so
	// the service checks itself.
	if len(os.Args) > 1 && os.Args[1] == healthcheckCommand {
		if err := healthcheck(context.Background(), os.Getenv("ADDR")); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
		return
	}

	// JSON from the first line. Converting a codebase's log calls in bulk
	// later is a PR nobody wants to review, and there are three of them now.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// NotifyContext rather than a signal channel: the cancellation is then
	// the same shape as every other deadline in the program.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Restore default signal handling as soon as the first one arrives, so a
	// second Ctrl-C during a fifteen-second drain kills the process instead
	// of being swallowed by the handler that is already shutting it down.
	go func() {
		<-ctx.Done()
		stop()
	}()

	if err := run(ctx, logger, os.Getenv("ADDR")); err != nil {
		logger.Error("exiting", "error", err)
		os.Exit(1)
	}
}

// run builds the server and serves until ctx is canceled.
func run(ctx context.Context, logger *slog.Logger, addr string) error {
	if addr == "" {
		addr = defaultAddr
	}

	// The listener is opened here rather than inside ListenAndServe so that a
	// port already in use fails now, with a clear error, instead of inside a
	// goroutine a moment later. ListenConfig rather than net.Listen so that a
	// signal arriving during startup cancels it.
	var config net.ListenConfig
	listener, err := config.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	server := &http.Server{
		Handler:           httpapi.Routes(),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	logger.Info("listening", "addr", listener.Addr().String())
	return serve(ctx, logger, server, listener, defaultGrace)
}

// serve runs the server until ctx is canceled, then drains in-flight
// requests for at most grace.
//
// Separated from run so it can be tested with a handler that is deliberately
// slow. Shutdown behavior is the kind of thing that is either verified or
// merely believed.
func serve(
	ctx context.Context,
	logger *slog.Logger,
	server *http.Server,
	listener net.Listener,
	grace time.Duration,
) error {
	failed := make(chan error, 1)
	go func() {
		// Serve always returns a non-nil error and closes the listener.
		// ErrServerClosed is the one that means "we asked it to stop", so it
		// is not a failure.
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failed <- err
		}
		close(failed)
	}()

	select {
	case err := <-failed:
		if err != nil {
			return fmt.Errorf("serving: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	logger.Info("draining", "grace", grace.String())

	// A fresh context: ctx is already canceled, and passing it would make
	// Shutdown give up immediately on the requests it is meant to wait for.
	// WithoutCancel rather than Background so context values survive.
	drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()

	// Shutdown closes listeners first, so nothing new is accepted, then waits
	// for idle connections. It does not wait for hijacked connections, so a
	// WebSocket or an upgraded connection needs its own shutdown path.
	shutdownErr := server.Shutdown(drain)

	// Both cases above can be ready at once — a genuine Serve failure landing
	// at the same moment as the signal — and select picks between ready cases
	// at random. Reading the channel here is what stops that error being
	// reported as a clean exit. Serve has returned by now, so this does not
	// block: the goroutine either sent or closed.
	if err := <-failed; err != nil {
		return fmt.Errorf("serving: %w", err)
	}
	if shutdownErr != nil {
		return fmt.Errorf("draining: %w", shutdownErr)
	}
	return nil
}

// healthcheck asks a running copy of the service whether it is up, and
// reports through the error.
//
// It only ever dials loopback. The host in ADDR says which interface the
// service binds to; it is not somewhere to send a request. Taking it at face
// value would make the healthcheck a client of whatever the environment
// named, which is a request forgery waiting for someone to set ADDR. So a
// loopback or unspecified host - ":8080" is what a container is given, so its
// port mapping can reach it - is probed on loopback, and anything else is
// refused rather than dialed.
func healthcheck(ctx context.Context, addr string) error {
	if addr == "" {
		addr = defaultAddr
	}
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parsing %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("%q is not a port", portText)
	}

	loopback := "127.0.0.1"
	switch ip := net.ParseIP(host); {
	case host == "" || host == "localhost" || (ip != nil && ip.IsUnspecified()):
	case ip != nil && ip.IsLoopback():
		if ip.To4() == nil {
			loopback = "::1" // bound to IPv6 loopback only, so probe that
		}
	default:
		return fmt.Errorf("ADDR host %q is not loopback or unspecified; "+
			"the healthcheck only probes this process", host)
	}

	ctx, cancel := context.WithTimeout(ctx, healthcheckTimeout)
	defer cancel()

	// gosec's taint analysis follows port from the environment into the URL
	// and cannot see that it was validated as an integer, or that the host is
	// a constant. Both suppressions below are for that and nothing wider.
	url := "http://" + net.JoinHostPort(loopback, strconv.Itoa(port)) + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) //nolint:gosec // G704: loopback host, validated port
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: loopback host, validated port
	if err != nil {
		return fmt.Errorf("probing %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", url, resp.Status)
	}
	return nil
}
