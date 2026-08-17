package tools

import (
	"context"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/config"
)

// implementedAddonKinds is the ground-truth set of addon kinds the
// kusoaddon chart renders a real workload + conn Secret for. Source:
// the $supported gate in
// operator/helm-charts/kusoaddon/templates/unsupported.yaml (one
// per-kind template each). Kinds outside this set render only a
// "pending" marker ConfigMap — the MCP allowlist must never offer them.
var implementedAddonKinds = []string{
	"postgres", "redis", "valkey", "mongodb", "mysql", "rabbitmq",
	"s3", "mailpit", "nats", "meilisearch", "clickhouse", "redpanda",
}

// reservedAddonKinds are reserved-but-not-implemented: creating one
// "succeeds" but deploys nothing. They must never appear in the
// allowlist, the tool description, or the kind arg's schema hint.
var reservedAddonKinds = []string{
	"memcached", "elasticsearch", "kafka", "cockroachdb", "couchdb",
}

// newFullSurfaceSession spins up the REAL full tool surface (everything
// Register wires) against an unroutable URL, for tests that only
// inspect tool metadata and never call anything.
func newFullSurfaceSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	cfg := &config.Config{URL: "http://127.0.0.1:1", Token: "test"}
	server := mcp.NewServer(&mcp.Implementation{Name: "kuso-mcp-test", Version: "test"}, nil)
	Register(server, cfg)

	serverT, clientT := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	sess, err := mcpClient.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func listAllTools(t *testing.T, sess *mcp.ClientSession) []*mcp.Tool {
	t.Helper()
	var tools []*mcp.Tool
	res, err := sess.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	tools = append(tools, res.Tools...)
	for res.NextCursor != "" {
		res, err = sess.ListTools(context.Background(), &mcp.ListToolsParams{Cursor: res.NextCursor})
		if err != nil {
			t.Fatalf("list tools (page): %v", err)
		}
		tools = append(tools, res.Tools...)
	}
	return tools
}

// TestAllowedAddonKindsMatchImplemented pins the manage_addon allowlist
// to exactly the chart-implemented kinds — no reserved placeholders, no
// implemented kind missing.
func TestAllowedAddonKindsMatchImplemented(t *testing.T) {
	want := map[string]bool{}
	for _, k := range implementedAddonKinds {
		want[k] = true
	}
	if !reflect.DeepEqual(allowedAddonKinds, want) {
		var missing, extra []string
		for k := range want {
			if !allowedAddonKinds[k] {
				missing = append(missing, k)
			}
		}
		for k := range allowedAddonKinds {
			if !want[k] {
				extra = append(extra, k)
			}
		}
		t.Fatalf("allowedAddonKinds drifted from the chart's implemented set\n  missing: %v\n  extra (reserved/unknown): %v", missing, extra)
	}
}

// TestManageAddonAdvertisesExactKinds asserts the tool description and
// the kind arg's jsonschema hint list every implemented kind and no
// reserved kind, so what the agent is told matches what the gate allows.
func TestManageAddonAdvertisesExactKinds(t *testing.T) {
	sess := newFullSurfaceSession(t)
	var desc string
	for _, tool := range listAllTools(t, sess) {
		if tool.Name == "manage_addon" {
			desc = tool.Description
		}
	}
	if desc == "" {
		t.Fatal("manage_addon tool not registered or has empty description")
	}

	field, ok := reflect.TypeOf(manageAddonArgs{}).FieldByName("Kind")
	if !ok {
		t.Fatal("manageAddonArgs has no Kind field")
	}
	kindHint := field.Tag.Get("jsonschema")

	for _, k := range implementedAddonKinds {
		if !strings.Contains(desc, k) {
			t.Errorf("manage_addon description omits implemented kind %q", k)
		}
		if !strings.Contains(kindHint, k) {
			t.Errorf("manage_addon kind jsonschema hint omits implemented kind %q", k)
		}
	}
	for _, k := range reservedAddonKinds {
		if strings.Contains(desc, k) {
			t.Errorf("manage_addon description advertises reserved kind %q", k)
		}
		if strings.Contains(kindHint, k) {
			t.Errorf("manage_addon kind jsonschema hint advertises reserved kind %q", k)
		}
	}
}

// snakeToken matches lowercase snake_case identifiers (≥1 underscore) —
// the shape of a tool name mentioned in prose. Every such mention in a
// tool description must be a tool that actually exists, so descriptions
// can never advertise phantom tools.
var snakeToken = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)

func TestToolDescriptionsReferenceOnlyRegisteredTools(t *testing.T) {
	sess := newFullSurfaceSession(t)
	tools := listAllTools(t, sess)
	registered := map[string]bool{}
	for _, tool := range tools {
		registered[tool.Name] = true
	}
	for _, tool := range tools {
		for _, tok := range snakeToken.FindAllString(tool.Description, -1) {
			if !registered[tok] {
				t.Errorf("tool %q description references %q, which is not a registered tool", tool.Name, tok)
			}
		}
	}
}

// TestReadmeToolCountMatchesRegistrations keeps the README's "N tools
// registered" claim honest against the live tool surface.
func TestReadmeToolCountMatchesRegistrations(t *testing.T) {
	sess := newFullSurfaceSession(t)
	got := len(listAllTools(t, sess))

	raw, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	m := regexp.MustCompile(`(\d+) tools registered`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Skip("README no longer states a tool count — nothing to check")
	}
	claimed, _ := strconv.Atoi(m[1])
	if claimed != got {
		t.Fatalf("README claims %d tools registered, but %d are registered", claimed, got)
	}
}
