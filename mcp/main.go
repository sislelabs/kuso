// kuso-mcp is a Model Context Protocol server for kuso.
//
// It exposes intent-grouped tools that let an MCP-speaking client (Claude
// Code, Cursor, Claude Desktop) drive a kuso PaaS instance: list and
// describe apps, deploy, troubleshoot, manage secrets, etc.
//
// The server reads KUSO_URL and KUSO_TOKEN from the environment; when
// either is unset it falls back to the kuso CLI's ~/.kuso/kuso.yaml +
// credentials.yaml (respecting the CLI's currentInstance). Pass
// --read-only to disable mutating tools.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/debug"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/config"
	"github.com/sislelabs/kuso/mcp/internal/tools"
)

const serverName = "kuso-mcp"

// serverVersion is stamped at release time with
// -ldflags "-X main.serverVersion=vX.Y.Z". Unstamped builds fall back to
// the module version or VCS revision so a client can still tell two
// builds apart.
var serverVersion = ""

func resolveVersion(stamped string, bi *debug.BuildInfo) string {
	if stamped != "" {
		return stamped
	}
	if bi == nil {
		return "dev"
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var rev, dirty string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if rev == "" {
		return "dev"
	}
	return "dev-" + rev + dirty
}

func main() {
	readOnly := flag.Bool("read-only", false, "disable mutating tools")
	flag.Parse()

	cfg, err := config.FromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "kuso-mcp: %v\n", err)
		os.Exit(1)
	}
	cfg.ReadOnly = *readOnly

	bi, _ := debug.ReadBuildInfo()
	server := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Version: resolveVersion(serverVersion, bi),
	}, nil)

	tools.Register(server, cfg)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("kuso-mcp: server failed: %v", err)
	}
}
