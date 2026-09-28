package handlers

import (
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"kuso/server/internal/httperr"
)

// writeErr is THE error writer for the HTTP API. Every error response
// is a JSON envelope:
//
//	{"error": "<human-readable message>", "code": "<machine code>", "requestId": "<id>"}
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
// "error", "code" and "requestId" are reserved — extras never override them.
func writeErrExtra(w http.ResponseWriter, status int, msg, code string, extra map[string]any) {
	httperr.WriteExtra(w, status, msg, code, extra)
}

// errCode derives the machine `code` from the HTTP status.
func errCode(status int) string { return httperr.Code(status) }

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

// kubeErrStatus maps a Kubernetes API error the caller caused to a 4xx:
// Invalid/BadRequest (a CR the schema rejects) -> 400, Conflict/
// AlreadyExists -> 409. ok is false for anything else, which stays a
// server fault. The kube message names the field, so it is passed through.
func kubeErrStatus(err error) (status int, ok bool) {
	switch {
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		return http.StatusBadRequest, true
	case apierrors.IsConflict(err), apierrors.IsAlreadyExists(err):
		return http.StatusConflict, true
	}
	return 0, false
}
