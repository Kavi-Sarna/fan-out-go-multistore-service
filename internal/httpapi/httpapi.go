// Package httpapi holds the HTTP surface: routes, handlers and middleware.
//
// It knows nothing about how the service is started or stopped. That belongs
// to the caller, which keeps this package testable with httptest and no
// listener at all.
package httpapi

import (
	"encoding/json"
	"net/http"
)

// Routes returns the handler for the whole API.
//
// Go 1.22 gave ServeMux method and wildcard patterns, so "GET /healthz" is a
// route rather than a switch inside a handler. That is enough for this
// service and one fewer dependency to keep current.
func Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	return mux
}

// healthz answers whether the process is up. Deliberately nothing more.
//
// A health endpoint that checks a database turns a slow dependency into an
// outage: the probe fails, Kubernetes restarts the pod, the new pod finds the
// same slow dependency. Readiness may depend on dependencies; liveness must
// not, and this is the one both will start out pointing at.
func healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	// The error is deliberately ignored: the only way it fails is a broken
	// connection, and there is nobody left to tell.
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
