package kusoCli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"kuso/pkg/kusoApi"
)

// Request-shape tests for the commands added by the 2026-10-15 parity
// pass. Each drives the real cobra tree against a fake server and
// asserts on what went over the wire.

type parityReq struct {
	Method, Path, Query, Auth string
	Body                      map[string]any
}

type parityServer struct {
	mu   sync.Mutex
	reqs []parityReq
}

func (p *parityServer) got() []parityReq {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]parityReq(nil), p.reqs...)
}

// find returns the first request with this method + path, or fails.
func (p *parityServer) find(t *testing.T, method, path string) parityReq {
	t.Helper()
	for _, r := range p.got() {
		if r.Method == method && r.Path == path {
			return r
		}
	}
	t.Fatalf("no %s %s; got %+v", method, path, p.got())
	return parityReq{}
}

func (p *parityServer) count(method, path string) int {
	n := 0
	for _, r := range p.got() {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

func newParityServer(t *testing.T, routes map[string]string) *parityServer {
	t.Helper()
	p := &parityServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		p.mu.Lock()
		p.reqs = append(p.reqs, parityReq{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), body})
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		resp, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	t.Cleanup(func() { api = nil })
	return p
}

// runParity runs one CLI invocation and resets every flag on the
// command it resolved to, so flag state can't leak between tests.
func runParity(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd, _, _ := rootCmd.Find(args)
	t.Cleanup(func() { resetCmdFlags(cmd) })
	var err error
	out := captureStdout(t, func() { _, err = runRoot(t, args...) })
	resetCmdFlags(cmd)
	return out, err
}

func resetCmdFlags(c *cobra.Command) {
	if c == nil {
		return
	}
	c.Flags().VisitAll(func(f *pflag.Flag) {
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			_ = sv.Replace([]string{})
		} else {
			_ = f.Value.Set(f.DefValue)
		}
		f.Changed = false
	})
	outputFormat = "table"
}

func nonTTY(t *testing.T) {
	orig := stdinIsTTYFn
	stdinIsTTYFn = func() bool { return false }
	t.Cleanup(func() { stdinIsTTYFn = orig })
}

func withStdin(t *testing.T, s string) {
	orig := secretStdin
	secretStdin = strings.NewReader(s)
	t.Cleanup(func() { secretStdin = orig })
}

func TestDBRowInsertSendsTextValuesAndNulls(t *testing.T) {
	p := newParityServer(t, map[string]string{
		"POST /api/projects/shop/addons/db/sql/rows": `{"columns":["id","title"],"row":[7,"milk"]}`,
	})
	out, err := runParity(t, "db", "row", "insert", "shop", "db", "--table", "todo",
		"--set", "title=milk=2%", "--set", "done=true", "--null", "note", "--database", "tenant_a")
	if err != nil {
		t.Fatal(err)
	}
	r := p.find(t, "POST", "/api/projects/shop/addons/db/sql/rows")
	if r.Query != "database=tenant_a" {
		t.Fatalf("query = %q", r.Query)
	}
	vals, _ := r.Body["values"].(map[string]any)
	title, _ := vals["title"].(map[string]any)
	note, _ := vals["note"].(map[string]any)
	if r.Body["schema"] != "public" || r.Body["table"] != "todo" || title["value"] != "milk=2%" || note["isNull"] != true {
		t.Fatalf("body = %+v", r.Body)
	}
	if !strings.Contains(out, "milk") {
		t.Fatalf("inserted row not printed: %s", out)
	}
}

func TestDBRowUpdateSendsSetAndPK(t *testing.T) {
	p := newParityServer(t, map[string]string{
		"PATCH /api/projects/shop/addons/db/sql/rows": `{"columns":["id","done"],"row":[1,true]}`,
	})
	if _, err := runParity(t, "db", "row", "update", "shop", "db", "--table", "todo", "--where", "id=1", "--set", "done=true"); err != nil {
		t.Fatal(err)
	}
	b := p.find(t, "PATCH", "/api/projects/shop/addons/db/sql/rows").Body
	pk, _ := b["pk"].(map[string]any)
	set, _ := b["set"].(map[string]any)
	if pk["id"].(map[string]any)["value"] != "1" || set["done"].(map[string]any)["value"] != "true" || b["values"] != nil {
		t.Fatalf("body = %+v", b)
	}
	if _, err := runParity(t, "db", "row", "update", "shop", "db", "--table", "todo", "--set", "done=true"); err == nil {
		t.Fatal("update without --where must fail before calling the server")
	}
}

func TestDBRowDeleteGuardedAndSendsPK(t *testing.T) {
	nonTTY(t)
	p := newParityServer(t, map[string]string{
		"DELETE /api/projects/shop/addons/db/sql/rows": `{"deleted":1}`,
	})
	if _, err := runParity(t, "db", "row", "delete", "shop", "db", "--table", "todo", "--where", "id=3"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("want non-TTY refusal, got %v", err)
	}
	if n := p.count("DELETE", "/api/projects/shop/addons/db/sql/rows"); n != 0 {
		t.Fatalf("delete reached the server without --yes (%d calls)", n)
	}
	if _, err := runParity(t, "db", "row", "delete", "shop", "db", "--table", "todo", "--where", "id=3", "--yes"); err != nil {
		t.Fatal(err)
	}
	pk, _ := p.find(t, "DELETE", "/api/projects/shop/addons/db/sql/rows").Body["pk"].(map[string]any)
	if pk["id"].(map[string]any)["value"] != "3" {
		t.Fatalf("pk = %+v", pk)
	}
}

func TestEnvGroupSetBranch(t *testing.T) {
	p := newParityServer(t, nil)
	if _, err := runParity(t, "env-group", "set-branch", "shop", "staging", "shop-api", "feat/x"); err != nil {
		t.Fatal(err)
	}
	r := p.find(t, "PATCH", "/api/projects/shop/env-groups/staging/services/api/branch")
	if r.Body["branch"] != "feat/x" {
		t.Fatalf("body = %+v", r.Body)
	}
}

const incidentSettingsJSON = `{"config":{"enabled":false,"triggerPod":true,"triggerAlert":true,"triggerNode":true,"maxConcurrent":3,"cooldownHours":1,"agentImage":"img:1"},"status":{"ccConfigured":true}}`

// PUT is a full replace: flags not passed must keep their current value.
func TestIncidentAgentSettingsSetIsReadModifyWrite(t *testing.T) {
	p := newParityServer(t, map[string]string{
		"GET /api/admin/settings/incident-agent": incidentSettingsJSON,
		"PUT /api/admin/settings/incident-agent": `{"ok":true}`,
	})
	if _, err := runParity(t, "incident-agent", "settings", "set", "--enabled", "--trigger-node=false"); err != nil {
		t.Fatal(err)
	}
	b := p.find(t, "PUT", "/api/admin/settings/incident-agent").Body
	if b["enabled"] != true || b["triggerNode"] != false || b["triggerPod"] != true ||
		b["maxConcurrent"] != float64(3) || b["cooldownHours"] != float64(1) || b["agentImage"] != "img:1" {
		t.Fatalf("PUT body lost untouched fields: %+v", b)
	}
}

func TestIncidentAgentSettingsSetDryRunDoesNotSave(t *testing.T) {
	p := newParityServer(t, map[string]string{"GET /api/admin/settings/incident-agent": incidentSettingsJSON})
	if _, err := runParity(t, "incident-agent", "settings", "set", "--enabled", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if n := p.count("PUT", "/api/admin/settings/incident-agent"); n != 0 {
		t.Fatalf("--dry-run saved (%d PUTs)", n)
	}
}

func TestIncidentAgentSetDiscordBody(t *testing.T) {
	p := newParityServer(t, map[string]string{"PUT /api/admin/settings/incident-agent/discord": `{"ok":true}`})
	withStdin(t, "bot-secret\n")
	if _, err := runParity(t, "incident-agent", "set-discord", "--bot-token-file", "-", "--channel", "123"); err != nil {
		t.Fatal(err)
	}
	b := p.find(t, "PUT", "/api/admin/settings/incident-agent/discord").Body
	if b["botToken"] != "bot-secret" || b["channelId"] != "123" {
		t.Fatalf("body = %+v", b)
	}
	if _, ok := b["kusoBotToken"]; ok {
		t.Fatalf("unset kusoBotToken must be omitted (server keeps stored): %+v", b)
	}

	// --clear-channel must send channelId:"" (nil would mean "keep").
	if _, err := runParity(t, "incident-agent", "set-discord", "--clear-channel"); err != nil {
		t.Fatal(err)
	}
	reqs := p.got()
	last := reqs[len(reqs)-1].Body
	if v, ok := last["channelId"]; !ok || v != "" {
		t.Fatalf("clear-channel body = %+v", last)
	}
}

func TestIncidentFeedbackSendsTextOnly(t *testing.T) {
	p := newParityServer(t, map[string]string{"POST /api/incidents/abc/feedback": `{"ok":true}`})
	if _, err := runParity(t, "incident", "feedback", "abc", "--text", "go ahead"); err != nil {
		t.Fatal(err)
	}
	b := p.find(t, "POST", "/api/incidents/abc/feedback").Body
	// "go ahead" as text must never be read as an approval decision.
	if b["text"] != "go ahead" || b["decision"] != nil {
		t.Fatalf("body = %+v", b)
	}
}

const pinnedJSON = `{"pinned":[{"namespace":"kuso-shop","pvc":"data-shop-db-0","kind":"addon","owner":"shop-db","size":"5Gi"}]}`

func TestNodeRemovePreflightRefusesBeforeRemove(t *testing.T) {
	p := newParityServer(t, map[string]string{"GET /api/kubernetes/nodes/w2/remove-preflight": pinnedJSON})
	_, err := runParity(t, "node", "remove", "w2", "--yes")
	if err == nil || !strings.Contains(err.Error(), "data-shop-db-0") || !strings.Contains(err.Error(), "--accept-data-loss") {
		t.Fatalf("want preflight refusal listing the volume, got %v", err)
	}
	if n := p.count("POST", "/api/kubernetes/nodes/w2/remove"); n != 0 {
		t.Fatalf("remove was called despite pinned volumes (%d)", n)
	}
}

func TestNodeRemoveDryRunOnlyPreflights(t *testing.T) {
	p := newParityServer(t, map[string]string{"GET /api/kubernetes/nodes/w2/remove-preflight": pinnedJSON})
	out, err := runParity(t, "node", "remove", "w2", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "data-shop-db-0") || p.count("POST", "/api/kubernetes/nodes/w2/remove") != 0 {
		t.Fatalf("dry-run output %q / requests %+v", out, p.got())
	}
}

func TestGroupUpdateKeepsUnpassedField(t *testing.T) {
	p := newParityServer(t, map[string]string{
		"GET /api/groups": `[{"id":"g1","name":"devs","description":"eng"}]`,
	})
	if _, err := runParity(t, "group", "update", "g1", "--description", "platform team"); err != nil {
		t.Fatal(err)
	}
	b := p.find(t, "PUT", "/api/groups/g1").Body
	if b["name"] != "devs" || b["description"] != "platform team" {
		t.Fatalf("body = %+v", b)
	}
}

func TestGroupTenancySetReplacesProjectsKeepsRole(t *testing.T) {
	p := newParityServer(t, map[string]string{
		"GET /api/groups/g1/tenancy": `{"instanceRole":"viewer","projectMemberships":[{"project":"old","role":"admin"}]}`,
	})
	if _, err := runParity(t, "group", "tenancy", "set", "g1", "--project", "shop=editor", "--project", "blog=viewer"); err != nil {
		t.Fatal(err)
	}
	b := p.find(t, "PUT", "/api/groups/g1/tenancy").Body
	ms, _ := b["projectMemberships"].([]any)
	if b["instanceRole"] != "viewer" || len(ms) != 2 || ms[0].(map[string]any)["project"] != "shop" {
		t.Fatalf("body = %+v", b)
	}
	if _, err := runParity(t, "group", "tenancy", "set", "g1", "--project", "shop=owner"); err == nil {
		t.Fatal("invalid role must be rejected client-side")
	}
}

func TestUserSetSendsOnlyPassedFields(t *testing.T) {
	p := newParityServer(t, nil)
	if _, err := runParity(t, "user", "set", "u1", "--active=false", "--first-name", "Ada"); err != nil {
		t.Fatal(err)
	}
	b := p.find(t, "PUT", "/api/users/id/u1").Body
	if b["isActive"] != false || b["firstName"] != "Ada" {
		t.Fatalf("body = %+v", b)
	}
	for _, k := range []string{"email", "lastName", "roleId"} {
		if _, ok := b[k]; ok {
			t.Fatalf("%s sent though not passed (would overwrite it): %+v", k, b)
		}
	}
}

func TestUserGetPath(t *testing.T) {
	p := newParityServer(t, map[string]string{"GET /api/users/id/u1": `{"id":"u1","username":"ada","email":"a@x.io","isActive":true}`})
	out, err := runParity(t, "user", "get", "u1")
	if err != nil {
		t.Fatal(err)
	}
	p.find(t, "GET", "/api/users/id/u1")
	if !strings.Contains(out, "ada") {
		t.Fatalf("output: %s", out)
	}
}

func TestProfileSetPasswordStdin(t *testing.T) {
	p := newParityServer(t, nil)
	withStdin(t, "old-pass\nnew-pass-123\n")
	if _, err := runParity(t, "user", "profile", "set-password", "--stdin"); err != nil {
		t.Fatal(err)
	}
	b := p.find(t, "PUT", "/api/users/profile/password").Body
	if b["currentPassword"] != "old-pass" || b["newPassword"] != "new-pass-123" {
		t.Fatalf("body = %+v", b)
	}
}

func TestTokenAdminRoutes(t *testing.T) {
	nonTTY(t)
	p := newParityServer(t, map[string]string{
		"GET /api/tokens":          `[{"id":"t1","name":"ci","isActive":true,"user":{"id":"u2","username":"bot"}}]`,
		"POST /api/tokens/user/u2": `{"name":"deploy","token":"jwt.x.y","expiresAt":"2027-01-01T00:00:00Z"}`,
	})
	out, err := runParity(t, "token", "list", "--all")
	if err != nil || !strings.Contains(out, "bot") {
		t.Fatalf("list --all: %v %s", err, out)
	}
	if _, err := runParity(t, "token", "create", "--name", "deploy", "--user", "u2"); err != nil {
		t.Fatal(err)
	}
	if p.find(t, "POST", "/api/tokens/user/u2").Body["name"] != "deploy" {
		t.Fatal("create --user body")
	}
	if _, err := runParity(t, "token", "revoke", "t1", "--any", "--yes"); err != nil {
		t.Fatal(err)
	}
	p.find(t, "DELETE", "/api/tokens/t1")
	if p.count("DELETE", "/api/tokens/my/t1") != 0 || p.count("POST", "/api/tokens/my") != 0 {
		t.Fatalf("admin variants hit the self-service routes: %+v", p.got())
	}
}

func TestDrainUpdateKeepsMaskedCredentials(t *testing.T) {
	p := newParityServer(t, map[string]string{
		"GET /api/drains/d1": `{"id":"d1","name":"g","type":"otlp","url":"https://a","project":"shop","enabled":true,` +
			`"headers":{"Authorization":"••••••••","X-Old":"••••••••"},"secret":"••••••••"}`,
		"PUT /api/drains/d1": `{}`,
	})
	if _, err := runParity(t, "drain", "update", "d1", "--url", "https://b", "--remove-header", "X-Old", "--header", "X-New=1", "--enabled=false"); err != nil {
		t.Fatal(err)
	}
	b := p.find(t, "PUT", "/api/drains/d1").Body
	h, _ := b["headers"].(map[string]any)
	// The masked values must go back verbatim: the server reads the
	// sentinel as "keep stored"; anything else overwrites the credential.
	if b["url"] != "https://b" || b["secret"] != "••••••••" || h["Authorization"] != "••••••••" ||
		h["X-New"] != "1" || h["X-Old"] != nil || b["enabled"] != false || b["project"] != "shop" || b["type"] != "otlp" {
		t.Fatalf("body = %+v", b)
	}
}

func TestHealthOrphansDelete(t *testing.T) {
	nonTTY(t)
	p := newParityServer(t, map[string]string{
		"GET /api/health/reconcile": `{"issues":[{"resource":"data-x-0","namespace":"kuso-shop","kind":"orphan_addon_pvc","summary":"left over"}]}`,
	})
	if _, err := runParity(t, "health", "orphans", "delete", "--kind", "orphan_addon_pvc", "--namespace", "kuso-shop", "--name", "data-x-0"); err == nil {
		t.Fatal("want non-TTY refusal without --yes")
	}
	if _, err := runParity(t, "health", "orphans", "delete", "--kind", "bogus", "--namespace", "n", "--name", "x", "--yes"); err == nil {
		t.Fatal("unknown kind must be rejected")
	}
	if _, err := runParity(t, "health", "orphans", "delete", "--kind", "orphan_addon_pvc", "--namespace", "kuso-shop", "--name", "data-x-0", "--yes"); err != nil {
		t.Fatal(err)
	}
	r := p.find(t, "DELETE", "/api/admin/orphans")
	if r.Query != "kind=orphan_addon_pvc&name=data-x-0&namespace=kuso-shop" {
		t.Fatalf("query = %q", r.Query)
	}
	before := p.count("DELETE", "/api/admin/orphans")
	if out, err := runParity(t, "health", "orphans", "delete", "--kind", "orphan_addon_pvc", "--namespace", "kuso-shop", "--name", "data-x-0", "--dry-run"); err != nil || !strings.Contains(out, "would delete") {
		t.Fatalf("dry-run on a flagged orphan: %v %s", err, out)
	}
	if _, err := runParity(t, "health", "orphans", "delete", "--kind", "orphan_addon_pvc", "--namespace", "kuso-shop", "--name", "not-flagged", "--dry-run"); err == nil {
		t.Fatal("dry-run on an unflagged resource must fail")
	}
	if p.count("DELETE", "/api/admin/orphans") != before {
		t.Fatal("--dry-run deleted")
	}
}

func TestReadOnlyParityRoutes(t *testing.T) {
	p := newParityServer(t, map[string]string{
		"GET /api/notifications/event-types":                   `[{"type":"build.failed","label":"Build failed","group":"build","defaultMention":"@here"}]`,
		"GET /api/kubernetes/storageclasses":                   `[{"metadata":{"name":"local-path","annotations":{"storageclass.kubernetes.io/is-default-class":"true"}},"provisioner":"rancher.io/local-path"}]`,
		"GET /api/kubernetes/domains":                          `["a.example.com","b.example.com"]`,
		"GET /api/github/install-url":                          `{"configured":true,"url":"https://github.com/apps/kuso/installations/new"}`,
		"GET /api/github/setup-status":                         `{"configured":true,"appSlug":"kuso-x","appId":"42"}`,
		"GET /api/kubernetes/envs/shop-api-staging/metrics":    `{"env":"shop-api-staging","pods":[{"pod":"p1","cpuMillicores":12,"memBytes":1048576}]}`,
		"GET /api/kubernetes/envs/shop-api-staging/timeseries": `{"env":"shop-api-staging","range":"24h","step":"5m","series":{"requests":[[1,2],[2,4]],"errors":[],"p95ms":[[1,30]]}}`,
		"GET /api/github/installations/7/repos/acme/mono/tree": `[{"path":"services","type":"tree"},{"path":"README.md","type":"blob","size":10}]`,
	})
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"notifications", "event-types"}, "build.failed"},
		{[]string{"get", "storageclasses"}, "local-path"},
		{[]string{"get", "domains"}, "b.example.com"},
		{[]string{"github", "status"}, "kuso-x"},
		{[]string{"service", "metrics", "shop", "api", "--env", "staging", "--range", "24h"}, "p1"},
		{[]string{"github", "tree", "acme/mono", "--installation-id", "7", "--ref", "main", "--path", "apps"}, "services/"},
	} {
		out, err := runParity(t, c.args...)
		if err != nil || !strings.Contains(out, c.want) {
			t.Errorf("%v: err=%v out=%s", c.args, err, out)
		}
	}
	if q := p.find(t, "GET", "/api/kubernetes/envs/shop-api-staging/timeseries").Query; q != "range=24h" {
		t.Errorf("timeseries query = %q", q)
	}
	if q := p.find(t, "GET", "/api/github/installations/7/repos/acme/mono/tree").Query; q != "branch=main&path=apps" {
		t.Errorf("tree query = %q", q)
	}
}

func TestInviteAcceptSendsNoBearer(t *testing.T) {
	p := newParityServer(t, map[string]string{"POST /api/invites/redeem": `{"access_token":"jwt"}`})
	withStdin(t, "s3cret-pass\n")
	if _, err := runParity(t, "invite", "accept", "tok123", "--username", "ada", "--email", "a@x.io", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	r := p.find(t, "POST", "/api/invites/redeem")
	if r.Auth != "" {
		t.Fatalf("the caller's session leaked onto the public redeem call: %q", r.Auth)
	}
	if r.Body["token"] != "tok123" || r.Body["username"] != "ada" || r.Body["email"] != "a@x.io" || r.Body["password"] != "s3cret-pass" {
		t.Fatalf("body = %+v", r.Body)
	}
}
