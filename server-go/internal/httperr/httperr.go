// Package httperr writes the API's JSON error envelope:
//
//	{"error": "<message>", "code": "<machine code>", "requestId": "<id>"}
//
// It is a leaf package so middleware outside internal/http/handlers
// (auth, CSRF, load shedding, the SPA fallback) answers in the same shape
// the handlers do.
package httperr

import (
	"encoding/json"
	"net/http"
)

// RequestIDHeader is set on every response by the router's request-id
// middleware. Write copies it into the body so a user can quote one id
// from either place.
const RequestIDHeader = "X-Request-Id"

// Write writes the envelope with the code derived from status.
func Write(w http.ResponseWriter, status int, msg string) {
	WriteExtra(w, status, msg, Code(status), nil)
}

// WriteExtra appends extra structured fields to the envelope. "error",
// "code" and "requestId" are reserved; extras never override them.
func WriteExtra(w http.ResponseWriter, status int, msg, code string, extra map[string]any) {
	payload := map[string]any{"error": msg}
	for k, v := range extra {
		payload[k] = v
	}
	payload["error"] = msg
	delete(payload, "code")
	if code != "" {
		payload["code"] = code
	}
	delete(payload, "requestId")
	if id := w.Header().Get(RequestIDHeader); id != "" {
		payload["requestId"] = id
	}
	b, err := json.Marshal(payload)
	if err != nil {
		// A caller-supplied extra that can't marshal; fall back to plain
		// text rather than an empty body.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(msg + "\n"))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

// Code derives the machine `code` from the HTTP status. Deliberately
// coarse; call sites that need a finer code pass it to WriteExtra.
func Code(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusMethodNotAllowed:
		return "method_not_allowed"
	case http.StatusConflict:
		return "conflict"
	case http.StatusGone:
		return "gone"
	case http.StatusRequestEntityTooLarge:
		return "too_large"
	case http.StatusUnprocessableEntity:
		return "invalid"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusServiceUnavailable:
		return "unavailable"
	}
	if status >= 500 {
		return "internal"
	}
	return "error"
}
