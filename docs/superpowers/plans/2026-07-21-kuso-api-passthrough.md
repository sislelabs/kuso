# `kuso api` Raw Passthrough Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `gh api`-style `kuso api <METHOD> <path>` command that hits any `/api/...` endpoint using the CLI's existing auth, so every server endpoint is reachable without a dedicated command.

**Architecture:** One new resty method `Raw(...)` on the existing `KusoClient` (reuses its bearer token + base URL), and one new cobra command `api.go` that parses METHOD/path/flags, builds the body from `-f`/`-F`/`--data`, executes via `Raw`, then pretty-prints (optionally jq-filtered) the response with a non-zero exit on non-2xx.

**Tech Stack:** Go, cobra, resty v2.16.5. Optional: `github.com/itchyny/gojq` for `--jq` (pure-Go, no cgo).

## Global Constraints

- CLI package is `kusoCli` (commands under `cli/cmd/kusoCli/`) + client `kusoApi` (`cli/pkg/kusoApi/`). Follow the existing one-file-per-resource pattern.
- Commands read the package-global `api *kusoApi.KusoClient` (defined `cli/cmd/kusoCli/root.go:21`, initialized `root.go:79`). A command must guard `if api == nil { return fmt.Errorf("not logged in; run 'kuso login' first") }` before use (see `audit.go:48`).
- resty methods return `(*resty.Response, error)`; the underlying `k.client` is a `*resty.Request` (`cli/pkg/kusoApi/main.go`). Use `k.client.Execute(method, path)` for arbitrary methods.
- After changing the CLI, rebuild: `cd cli && go build -o /tmp/kuso ./cmd`. For local e2e, also refresh `dist/kuso-darwin-arm64`.
- `--jq` is OPTIONAL. If the new `gojq` dependency is unwanted, skip Task 4 entirely; Tasks 1–3, 5 stand alone.

---

### Task 1: `Raw` client method

**Files:**
- Create: `cli/pkg/kusoApi/raw.go`
- Test: `cli/pkg/kusoApi/raw_test.go`

**Interfaces:**
- Consumes: `KusoClient.client` (`*resty.Request`), already auth-configured by `Init`/`SetApiUrl`.
- Produces: `func (k *KusoClient) Raw(method, path string, body []byte, headers map[string]string) (*resty.Response, error)` — `method` is upper-cased HTTP verb; `path` is passed through as-is (caller normalizes); `body` nil means no body; `headers` may be nil.

- [ ] **Step 1: Write the failing test**

```go
// cli/pkg/kusoApi/raw_test.go
package kusoApi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRaw_GETReturnsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/projects" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok123" {
			t.Errorf("missing/wrong auth header: %q", got)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	k := &KusoClient{}
	k.Init(srv.URL, "tok123")

	resp, err := k.Raw("GET", "/api/projects", nil, nil)
	if err != nil {
		t.Fatalf("Raw returned error: %v", err)
	}
	if resp.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode())
	}
	if string(resp.Body()) != `{"ok":true}` {
		t.Fatalf("body = %s", resp.Body())
	}
}

func TestRaw_POSTSendsBodyAndHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		if string(buf) != `{"branch":"main"}` {
			t.Errorf("body = %s", buf)
		}
		if r.Header.Get("X-Test") != "1" {
			t.Errorf("custom header missing")
		}
		w.WriteHeader(201)
	}))
	defer srv.Close()

	k := &KusoClient{}
	k.Init(srv.URL, "tok")
	resp, err := k.Raw("POST", "/api/x", []byte(`{"branch":"main"}`), map[string]string{"X-Test": "1"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.StatusCode() != 201 {
		t.Fatalf("status = %d", resp.StatusCode())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cli && go test ./pkg/kusoApi/ -run TestRaw -v`
Expected: FAIL — `k.Raw undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
// cli/pkg/kusoApi/raw.go

// raw.go — a gh-api-style passthrough. `kuso api <METHOD> <path>` uses
// this to hit any /api endpoint with the CLI's configured bearer token,
// so a new server endpoint is reachable before it gets a typed method.

package kusoApi

import (
	"strings"

	"github.com/go-resty/resty/v2"
)

// Raw executes an arbitrary request against the configured instance.
// method is a case-insensitive HTTP verb; path is used verbatim (the
// caller normalizes the leading slash / "/api" prefix). body nil ->
// no body; headers nil -> none. Returns the raw *resty.Response so the
// caller decides how to render it.
func (k *KusoClient) Raw(method, path string, body []byte, headers map[string]string) (*resty.Response, error) {
	req := k.client
	for key, val := range headers {
		req.SetHeader(key, val)
	}
	if body != nil {
		// Default content type for a JSON passthrough; a caller-supplied
		// Content-Type header above still wins (SetHeader ran first, but
		// SetBody won't override an explicit header).
		if _, ok := headers["Content-Type"]; !ok {
			req.SetHeader("Content-Type", "application/json")
		}
		req.SetBody(body)
	}
	return req.Execute(strings.ToUpper(method), path)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd cli && go test ./pkg/kusoApi/ -run TestRaw -v`
Expected: PASS (both subtests).

- [ ] **Step 5: Commit**

```bash
git add cli/pkg/kusoApi/raw.go cli/pkg/kusoApi/raw_test.go
git commit -m "feat(cli): add KusoClient.Raw passthrough method

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Body assembly from `-f`/`-F`/`--data` + path/method helpers

**Files:**
- Create: `cli/cmd/kusoCli/api_body.go`
- Test: `cli/cmd/kusoCli/api_body_test.go`

**Interfaces:**
- Produces:
  - `func normalizeAPIPath(p string) string` — ensures the path targets the API: `"projects"` → `"/api/projects"`, `"/api/x"` → `"/api/x"`, `"/foo"` → `"/api/foo"` (leading slash added, `/api` prefix added if absent).
  - `func validateMethod(m string) (string, error)` — upper-cases and allows only GET/POST/PUT/PATCH/DELETE; error otherwise.
  - `func buildBody(dataFlag string, fFields, capFFields []string) ([]byte, error)` — mutually exclusive: if `dataFlag != ""` returns it as bytes (or reads a file when it starts with `@`); else assembles `-f` (coerced) and `-F` (string) fields into a JSON object. `-f value` coercion: integer → number, `true`/`false` → bool, `null` → null, `@file` → file contents (raw string), else string. Returns nil when all inputs are empty. Errors if both `dataFlag` and any field are set.

- [ ] **Step 1: Write the failing test**

```go
// cli/cmd/kusoCli/api_body_test.go
package kusoCli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeAPIPath(t *testing.T) {
	cases := map[string]string{
		"projects":     "/api/projects",
		"/api/x":       "/api/x",
		"/foo":         "/api/foo",
		"api/y":        "/api/y",
	}
	for in, want := range cases {
		if got := normalizeAPIPath(in); got != want {
			t.Errorf("normalizeAPIPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateMethod(t *testing.T) {
	if m, err := validateMethod("post"); err != nil || m != "POST" {
		t.Errorf("post -> %q, %v", m, err)
	}
	if _, err := validateMethod("FROBNICATE"); err == nil {
		t.Error("expected error for bad method")
	}
}

func TestBuildBody_Fields(t *testing.T) {
	b, err := buildBody("", []string{"count=3", "enabled=true", "name=foo"}, []string{"id=007"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not json: %s", b)
	}
	if m["count"].(float64) != 3 || m["enabled"] != true || m["name"] != "foo" || m["id"] != "007" {
		t.Fatalf("coercion wrong: %#v", m)
	}
}

func TestBuildBody_DataFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "b.json")
	_ = os.WriteFile(f, []byte(`{"x":1}`), 0o600)
	b, err := buildBody("@"+f, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"x":1}` {
		t.Fatalf("data-file body = %s", b)
	}
}

func TestBuildBody_MutuallyExclusive(t *testing.T) {
	if _, err := buildBody(`{"a":1}`, []string{"b=2"}, nil); err == nil {
		t.Error("expected error when --data and -f both set")
	}
}

func TestBuildBody_Empty(t *testing.T) {
	b, err := buildBody("", nil, nil)
	if err != nil || b != nil {
		t.Fatalf("empty inputs should yield nil body, got %v %v", b, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cli && go test ./cmd/kusoCli/ -run 'TestNormalizeAPIPath|TestValidateMethod|TestBuildBody' -v`
Expected: FAIL — undefined `normalizeAPIPath`, `validateMethod`, `buildBody`.

- [ ] **Step 3: Write minimal implementation**

```go
// cli/cmd/kusoCli/api_body.go

// api_body.go — request-body + path/method assembly for `kuso api`.
// Split out from api.go so the coercion rules are unit-testable without
// a cobra command or a live server.

package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// normalizeAPIPath makes a user-supplied path target the API surface.
// "projects" -> "/api/projects"; an existing /api prefix is kept.
func normalizeAPIPath(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasPrefix(p, "/api/") && p != "/api" {
		p = "/api" + p
	}
	return p
}

var allowedMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

func validateMethod(m string) (string, error) {
	up := strings.ToUpper(m)
	if !allowedMethods[up] {
		return "", fmt.Errorf("unsupported method %q (use GET|POST|PUT|PATCH|DELETE)", m)
	}
	return up, nil
}

// coerceFieldValue applies gh-style coercion to a -f value: an integer
// becomes a JSON number, true/false a bool, null a null, @file the raw
// file contents (as a string), anything else a string.
func coerceFieldValue(v string) (any, error) {
	switch {
	case v == "true":
		return true, nil
	case v == "false":
		return false, nil
	case v == "null":
		return nil, nil
	case strings.HasPrefix(v, "@"):
		b, err := os.ReadFile(v[1:])
		if err != nil {
			return nil, fmt.Errorf("read field file %q: %w", v[1:], err)
		}
		return string(b), nil
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n, nil
	}
	return v, nil
}

// buildBody assembles the request body. --data (raw JSON, or @file) is
// mutually exclusive with -f/-F. -f coerces values; -F keeps them as
// strings. Returns nil when there is no body.
func buildBody(dataFlag string, fFields, capFFields []string) ([]byte, error) {
	hasFields := len(fFields) > 0 || len(capFFields) > 0
	if dataFlag != "" && hasFields {
		return nil, fmt.Errorf("--data/-d cannot be combined with -f/-F")
	}
	if dataFlag != "" {
		if strings.HasPrefix(dataFlag, "@") {
			b, err := os.ReadFile(dataFlag[1:])
			if err != nil {
				return nil, fmt.Errorf("read --data file %q: %w", dataFlag[1:], err)
			}
			return b, nil
		}
		return []byte(dataFlag), nil
	}
	if !hasFields {
		return nil, nil
	}
	obj := map[string]any{}
	for _, f := range fFields {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return nil, fmt.Errorf("-f %q must be key=value", f)
		}
		cv, err := coerceFieldValue(v)
		if err != nil {
			return nil, err
		}
		obj[k] = cv
	}
	for _, f := range capFFields {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return nil, fmt.Errorf("-F %q must be key=value", f)
		}
		obj[k] = v // always string
	}
	return json.Marshal(obj)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd cli && go test ./cmd/kusoCli/ -run 'TestNormalizeAPIPath|TestValidateMethod|TestBuildBody' -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Commit**

```bash
git add cli/cmd/kusoCli/api_body.go cli/cmd/kusoCli/api_body_test.go
git commit -m "feat(cli): body/path/method assembly for kuso api

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: The `api` cobra command (no --jq yet)

**Files:**
- Create: `cli/cmd/kusoCli/api.go`
- Test: `cli/cmd/kusoCli/api_test.go`

**Interfaces:**
- Consumes: `normalizeAPIPath`, `validateMethod`, `buildBody` (Task 2); `api.Raw` (Task 1); package-global `api`.
- Produces: `apiCmd` registered on `rootCmd`; a helper `func renderAPIResponse(out io.Writer, body []byte, include bool, status int, header http.Header) error` that pretty-prints JSON (else raw) and, when `include`, writes a status line + headers first. Exit behavior on non-2xx is handled in `RunE` (returns an error after printing).

- [ ] **Step 1: Write the failing test**

```go
// cli/cmd/kusoCli/api_test.go
package kusoCli

import (
	"bytes"
	"net/http"
	"testing"
)

func TestRenderAPIResponse_PrettyJSON(t *testing.T) {
	var buf bytes.Buffer
	err := renderAPIResponse(&buf, []byte(`{"b":2,"a":1}`), false, 200, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Pretty-printed, indented output.
	if !bytes.Contains(buf.Bytes(), []byte("\n  ")) {
		t.Fatalf("expected indented JSON, got:\n%s", buf.String())
	}
}

func TestRenderAPIResponse_RawNonJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := renderAPIResponse(&buf, []byte("plain text"), false, 200, nil); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "plain text\n" {
		t.Fatalf("raw passthrough wrong: %q", buf.String())
	}
}

func TestRenderAPIResponse_Include(t *testing.T) {
	var buf bytes.Buffer
	h := http.Header{"Content-Type": []string{"application/json"}}
	if err := renderAPIResponse(&buf, []byte(`{}`), true, 201, h); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("HTTP 201")) ||
		!bytes.Contains(buf.Bytes(), []byte("Content-Type: application/json")) {
		t.Fatalf("include header output wrong:\n%s", buf.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cli && go test ./cmd/kusoCli/ -run TestRenderAPIResponse -v`
Expected: FAIL — `renderAPIResponse` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// cli/cmd/kusoCli/api.go

// api.go — `kuso api <METHOD> <path>`, a gh-api-style escape hatch that
// hits any /api endpoint with the CLI's configured auth. Keeps the CLI
// small: an endpoint is reachable here the moment it ships, before it
// earns a dedicated typed command.

package kusoCli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/spf13/cobra"
)

var (
	apiData    string
	apiFields  []string
	apiCapF    []string
	apiHeaders []string
	apiInclude bool
	apiMethodX string
)

// renderAPIResponse pretty-prints JSON bodies (indent 2), passes other
// bodies through raw, and — when include is set — writes a status line
// and sorted headers first. Always newline-terminates.
func renderAPIResponse(out io.Writer, body []byte, include bool, status int, header http.Header) error {
	if include {
		fmt.Fprintf(out, "HTTP %d\n", status)
		keys := make([]string, 0, len(header))
		for k := range header {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, v := range header[k] {
				fmt.Fprintf(out, "%s: %s\n", k, v)
			}
		}
		fmt.Fprintln(out)
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, body, "", "  ") == nil && pretty.Len() > 0 {
		pretty.WriteByte('\n')
		_, err := out.Write(pretty.Bytes())
		return err
	}
	_, err := fmt.Fprintln(out, string(body))
	return err
}

var apiCmd = &cobra.Command{
	Use:   "api <METHOD> <path>",
	Short: "Call any kuso API endpoint directly (gh-api style).",
	Long: `Send an authenticated request to any /api endpoint using the
credentials from 'kuso login'. Useful for endpoints without a dedicated
command, scripting, and debugging.

The path may omit the leading slash and the /api prefix:
"projects" is treated as "/api/projects".`,
	Example: `  kuso api GET /api/projects
  kuso api GET projects
  kuso api POST /api/projects/acme/services/web/builds -f branch=main
  kuso api DELETE /api/projects/acme/grants/12
  kuso api POST /api/x --data '{"a":1}'
  kuso api GET projects -i`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		// METHOD path  OR  path (with -X METHOD)
		var methodArg, pathArg string
		if len(args) == 2 {
			methodArg, pathArg = args[0], args[1]
		} else if apiMethodX != "" {
			methodArg, pathArg = apiMethodX, args[0]
		} else {
			methodArg, pathArg = "GET", args[0]
		}
		method, err := validateMethod(methodArg)
		if err != nil {
			return err
		}
		path := normalizeAPIPath(pathArg)

		body, err := buildBody(apiData, apiFields, apiCapF)
		if err != nil {
			return err
		}
		headers := map[string]string{}
		for _, h := range apiHeaders {
			k, v, ok := cutHeader(h)
			if !ok {
				return fmt.Errorf("-H %q must be Key:Value", h)
			}
			headers[k] = v
		}

		resp, err := api.Raw(method, path, body, headers)
		if err != nil {
			return err
		}
		if rerr := renderAPIResponse(cmd.OutOrStdout(), resp.Body(), apiInclude, resp.StatusCode(), resp.Header()); rerr != nil {
			return rerr
		}
		if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
			return fmt.Errorf("HTTP %d", resp.StatusCode())
		}
		return nil
	},
}

// cutHeader splits "Key: Value" (optional space after colon).
func cutHeader(h string) (string, string, bool) {
	for i := 0; i < len(h); i++ {
		if h[i] == ':' {
			k := h[:i]
			v := h[i+1:]
			if len(v) > 0 && v[0] == ' ' {
				v = v[1:]
			}
			return k, v, k != ""
		}
	}
	return "", "", false
}

func init() {
	apiCmd.Flags().StringVarP(&apiData, "data", "d", "", "raw JSON request body (or @file.json)")
	apiCmd.Flags().StringArrayVarP(&apiFields, "field", "f", nil, "typed field key=value (repeatable; coerces numbers/bools/null; @file reads a value)")
	apiCmd.Flags().StringArrayVarP(&apiCapF, "raw-field", "F", nil, "string field key=value (repeatable; no coercion)")
	apiCmd.Flags().StringArrayVarP(&apiHeaders, "header", "H", nil, "extra request header Key:Value (repeatable)")
	apiCmd.Flags().BoolVarP(&apiInclude, "include", "i", false, "print response status + headers before the body")
	apiCmd.Flags().StringVarP(&apiMethodX, "method", "X", "", "HTTP method (alternative to the positional METHOD)")
	rootCmd.AddCommand(apiCmd)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd cli && go test ./cmd/kusoCli/ -run TestRenderAPIResponse -v`
Expected: PASS (all three subtests).

- [ ] **Step 5: Build the CLI to confirm the command wires up**

Run: `cd cli && go build -o /tmp/kuso ./cmd && /tmp/kuso api --help`
Expected: help text for `kuso api <METHOD> <path>` listing the `-d/-f/-F/-H/-i/-X` flags. No build errors.

- [ ] **Step 6: Commit**

```bash
git add cli/cmd/kusoCli/api.go cli/cmd/kusoCli/api_test.go
git commit -m "feat(cli): add 'kuso api' raw passthrough command

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4 (OPTIONAL): `--jq` response filtering

Skip this task entirely if adding the `github.com/itchyny/gojq` dependency is unwanted. Tasks 1–3, 5 do not depend on it.

**Files:**
- Modify: `cli/cmd/kusoCli/api.go` (add `--jq` flag + filter step)
- Create: `cli/cmd/kusoCli/api_jq.go`
- Test: `cli/cmd/kusoCli/api_jq_test.go`
- Modify: `cli/go.mod`, `cli/go.sum` (via `go get`)

**Interfaces:**
- Consumes: response body bytes from Task 3's `RunE`.
- Produces: `func applyJQ(body []byte, expr string) ([]byte, error)` — parses `body` as JSON, runs the gojq program, and returns the results as newline-separated JSON values. Error if `body` isn't JSON or the expression fails to compile.

- [ ] **Step 1: Add the dependency**

Run: `cd cli && go get github.com/itchyny/gojq@latest`
Expected: `go.mod`/`go.sum` updated with `github.com/itchyny/gojq`.

- [ ] **Step 2: Write the failing test**

```go
// cli/cmd/kusoCli/api_jq_test.go
package kusoCli

import "testing"

func TestApplyJQ_Field(t *testing.T) {
	out, err := applyJQ([]byte(`{"name":"web","port":3000}`), ".name")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "\"web\"\n" {
		t.Fatalf("jq .name = %q", out)
	}
}

func TestApplyJQ_Iterate(t *testing.T) {
	out, err := applyJQ([]byte(`{"items":[{"n":"a"},{"n":"b"}]}`), ".items[].n")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "\"a\"\n\"b\"\n" {
		t.Fatalf("jq iterate = %q", out)
	}
}

func TestApplyJQ_NotJSON(t *testing.T) {
	if _, err := applyJQ([]byte("not json"), ".x"); err == nil {
		t.Error("expected error for non-JSON body")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd cli && go test ./cmd/kusoCli/ -run TestApplyJQ -v`
Expected: FAIL — `applyJQ` undefined.

- [ ] **Step 4: Write minimal implementation**

```go
// cli/cmd/kusoCli/api_jq.go

// api_jq.go — optional --jq filtering for `kuso api`, backed by gojq
// (pure-Go, no cgo). Isolated in its own file so the dependency is easy
// to drop.

package kusoCli

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/itchyny/gojq"
)

// applyJQ runs a jq expression over a JSON body and returns each result
// as a newline-terminated JSON value.
func applyJQ(body []byte, expr string) ([]byte, error) {
	var input any
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, fmt.Errorf("response is not JSON, cannot apply --jq: %w", err)
	}
	query, err := gojq.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid jq expression: %w", err)
	}
	var out bytes.Buffer
	iter := query.Run(input)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, ok := v.(error); ok {
			return nil, fmt.Errorf("jq: %w", err)
		}
		enc, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		out.Write(enc)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}
```

- [ ] **Step 5: Wire `--jq` into the command**

In `cli/cmd/kusoCli/api.go`, add the flag var and registration, and filter the body before rendering.

Add to the `var (...)` block:
```go
	apiJQ      string
```
Add to `init()`:
```go
	apiCmd.Flags().StringVar(&apiJQ, "jq", "", "filter the JSON response through a jq expression")
```
In `RunE`, replace the render+exit tail (from `if rerr := renderAPIResponse(...)` onward) with:
```go
		out := resp.Body()
		if apiJQ != "" {
			filtered, jerr := applyJQ(out, apiJQ)
			if jerr != nil {
				return jerr
			}
			out = filtered
		}
		if rerr := renderAPIResponse(cmd.OutOrStdout(), out, apiInclude, resp.StatusCode(), resp.Header()); rerr != nil {
			return rerr
		}
		if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
			return fmt.Errorf("HTTP %d", resp.StatusCode())
		}
		return nil
```

- [ ] **Step 6: Run tests + build**

Run: `cd cli && go test ./cmd/kusoCli/ -run 'TestApplyJQ|TestRenderAPIResponse' -v && go build -o /tmp/kuso ./cmd`
Expected: PASS + clean build.

- [ ] **Step 7: Commit**

```bash
git add cli/cmd/kusoCli/api.go cli/cmd/kusoCli/api_jq.go cli/cmd/kusoCli/api_jq_test.go cli/go.mod cli/go.sum
git commit -m "feat(cli): add --jq filtering to kuso api

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Full package test pass + docs + CLI rebuild

**Files:**
- Modify: `dist/kuso-darwin-arm64` (rebuilt binary, if committed for local e2e — check whether it's tracked first)
- Modify: `CLAUDE.md` (add `kuso api` to the cluster-inspection guidance table)

- [ ] **Step 1: Run the whole CLI test suite**

Run: `cd cli && go test ./... 2>&1 | tail -20`
Expected: all packages PASS (or unchanged pre-existing failures — note any that were already failing before this work).

- [ ] **Step 2: Manual smoke against a live instance (if `agent-target.local.json` exists)**

Read `agent-target.local.json` for the CLI binary path + instance. Then:
Run: `/tmp/kuso api GET /api/projects -i`
Expected: `HTTP 200` + a JSON projects payload. Try a bad path:
Run: `/tmp/kuso api GET /api/does-not-exist; echo "exit=$?"`
Expected: prints `HTTP 404` note to stderr, `exit=1`.

- [ ] **Step 3: Document the command in CLAUDE.md**

In the cluster-inspection table under "## Cluster inspection", add a row:
```markdown
| Hit any API endpoint directly    | `kuso api <METHOD> <path> [-f k=v] [--data @f.json] [--jq expr]`          |
```
And under the "The CLI now has web-UI parity" paragraph, note: "`kuso api` is the raw escape hatch — any `/api` endpoint is reachable even before it has a dedicated command."

- [ ] **Step 4: Rebuild the tracked dist binary if it is version-controlled**

Run: `git ls-files --error-unmatch dist/kuso-darwin-arm64 2>/dev/null && echo TRACKED || echo UNTRACKED`
- If TRACKED: `cd cli && go build -o ../dist/kuso-darwin-arm64 ./cmd`
- If UNTRACKED: skip (the CLAUDE.md note about keeping `dist/` current still applies; do not add it to git if it wasn't tracked).

- [ ] **Step 5: Commit**

```bash
git add CLAUDE.md
git ls-files --error-unmatch dist/kuso-darwin-arm64 2>/dev/null && git add dist/kuso-darwin-arm64
git commit -m "docs(cli): document 'kuso api'; refresh CLI binary

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**1. Spec coverage** (Piece 1 of the design):
- Full `gh api`-style command with `-f`/`-F`/`--data`/`--jq`/`-H`/`-i`/`-X` → Tasks 2, 3, 4. ✓
- Reuses `KusoClient` bearer auth → Task 1 (`Raw` uses `k.client`). ✓
- `-f`/`--data` mutually exclusive, coercion, `@file` → Task 2 (`buildBody`, `coerceFieldValue`). ✓
- Pretty-print JSON, raw passthrough, `-i` headers → Task 3 (`renderAPIResponse`). ✓
- Non-2xx → non-zero exit, body still printed → Task 3 `RunE` tail. ✓
- `--jq` optional (droppable dep) → Task 4 marked OPTIONAL. ✓
- Out of scope (no `--paginate`, no templating) → not implemented. ✓

**2. Placeholder scan:** No TBD/TODO; every code step has complete code; commands have expected output. ✓

**3. Type consistency:** `Raw(method, path string, body []byte, headers map[string]string)` defined in Task 1, consumed identically in Task 3. `buildBody(dataFlag string, fFields, capFFields []string)` defined and called identically. `renderAPIResponse(out, body, include, status, header)` signature consistent between Task 3 impl and Task 4 edit. `applyJQ(body []byte, expr string)` consistent. ✓

Note for the implementer: Task 4 edits the exact `RunE` tail introduced in Task 3 — if executing out of order, apply Task 3 first.
