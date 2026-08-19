package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
)

// writeErr is THE error writer for the HTTP API. Every error response
// is a JSON envelope:
//
//	{"error": "<human-readable message>", "code": "<machine code>"}
//
// so the web client, CLI, MCP server and scripts all parse one shape
// instead of sniffing free text. The message is the same string that
// used to go through http.Error — content is preserved, only the
// framing changed. Extra structured fields (e.g. the shadowed-secret
// hint) ride along via writeErrExtra without breaking the envelope.
func writeErr(w http.ResponseWriter, status int, msg string) {
	writeErrCode(w, status, msg, errCode(status))
}

// writeErrCode writes the envelope with an explicit machine code,
// for the cases where the code carries more than the status does
// (e.g. "shadowed" on a 409).
func writeErrCode(w http.ResponseWriter, status int, msg, code string) {
	writeErrExtra(w, status, msg, code, nil)
}

// writeErrExtra appends extra structured fields to the envelope.
// "error" and "code" are reserved — extras never override them.
func writeErrExtra(w http.ResponseWriter, status int, msg, code string, extra map[string]any) {
	payload := map[string]any{"error": msg}
	if code != "" {
		payload["code"] = code
	}
	for k, v := range extra {
		if k == "error" || k == "code" {
			continue
		}
		payload[k] = v
	}
	b, err := json.Marshal(payload)
	if err != nil {
		// Marshal of map[string]string-ish payloads can't realistically
		// fail; fall back to plain text rather than an empty 500 body.
		// (Not http.Error / writeErr — this IS the writer.)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(status)
		w.Write([]byte(msg + "\n"))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	// http.Error set nosniff; keep that property.
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	w.Write(append(b, '\n'))
}

// errCode derives the machine `code` from the HTTP status. Kept
// deliberately coarse — clients that need finer distinctions get them
// via writeErrCode at the site (e.g. "shadowed").
func errCode(status int) string {
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

// notFoundMsg builds the 404 message. When the wrapped error carries
// more than the bare sentinel ("projects: not found: service p/s"),
// pass it through — same courtesy conflicts already get. Otherwise
// name the resource kind so a route with four path params doesn't
// answer with an anonymous "not found".
func notFoundMsg(err, sentinel error, kind string) string {
	if err != nil && sentinel != nil && err.Error() != sentinel.Error() {
		return err.Error()
	}
	if kind == "" {
		kind = "resource"
	}
	return kind + " not found"
}

// kindFromOp extracts the resource kind from a fail-helper op string
// ("get service" → "service"). Ops are verb-first by convention, so
// the last token is the noun.
func kindFromOp(op string) string {
	if i := strings.LastIndexByte(op, ' '); i >= 0 && i+1 < len(op) {
		return op[i+1:]
	}
	if op != "" {
		return op
	}
	return "resource"
}
