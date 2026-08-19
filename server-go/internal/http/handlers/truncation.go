package handlers

import "net/http"

// Truncation signalling for list endpoints (agent-use W4).
//
// Several high-volume list endpoints cap their result set (builds,
// audit, service errors, …). A capped response used to be
// indistinguishable from a complete one — an agent (or the CLI)
// reading 100 rows couldn't tell "that's all there is" from "there's
// more you didn't get". Some of these endpoints return bare JSON
// arrays whose wire shape is frozen, so the signal lives in response
// headers instead of the body:
//
//	X-Kuso-Truncated: true      — the response was cut; more rows exist
//	X-Kuso-Next-Offset: <n>     — offset-paged endpoints: pass as ?offset=
//	X-Kuso-Next-After: <id>     — keyset-paged endpoints: pass as ?after=
//
// Absent headers mean the response is complete. Headers are additive
// and ignorable, so pre-existing clients are unaffected.
const (
	headerTruncated  = "X-Kuso-Truncated"
	headerNextOffset = "X-Kuso-Next-Offset"
	headerNextAfter  = "X-Kuso-Next-After"
)

// setTruncationHeaders stamps the truncation marker plus the
// pagination cursor for the next page. nextHeader picks the cursor
// style (headerNextOffset or headerNextAfter); an empty nextValue
// omits the cursor and only marks truncation.
func setTruncationHeaders(w http.ResponseWriter, nextHeader, nextValue string) {
	w.Header().Set(headerTruncated, "true")
	if nextValue != "" {
		w.Header().Set(nextHeader, nextValue)
	}
}
