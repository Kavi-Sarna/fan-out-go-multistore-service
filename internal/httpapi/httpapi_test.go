package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kavi-sarna/fan-out-go-multistore-service/internal/httpapi"
)

func TestHealthzReportsOK(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	httpapi.Routes().ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var body map[string]string
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, want ok", body["status"])
	}
}

// The wrong method on a real route must not be a 404: a client that sees 404
// concludes the endpoint does not exist and stops looking.
func TestHealthzRejectsOtherMethods(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	httpapi.Routes().ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/healthz", nil))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}

func TestUnknownRouteIsNotFound(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	httpapi.Routes().ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/nope", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}
