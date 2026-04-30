// kuso-mcp is a Model Context Protocol server for kuso.
//
// It exposes intent-grouped tools that let an MCP-speaking client (Claude
// Code, Cursor, Claude Desktop) drive a kuso PaaS instance: list and
// describe apps, deploy, troubleshoot, manage secrets, etc.
//
// The server reads KUSO_URL and KUSO_TOKEN from the environment. Pass
// --read-only to disable mutating tools.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/config"
	"github.com/sislelabs/kuso/mcp/internal/tools"
)

const (
	serverName    = "kuso-mcp"
	serverVersion = "v0.1.0-dev"
)

func main() {
	readOnly := flag.Bool("read-only", false, "disable mutating tools")
	flag.Parse()

	cfg, err := config.FromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "kuso-mcp: %v\n", err)
		os.Exit(1)
	}
	cfg.ReadOnly = *readOnly

	server := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}, nil)

	tools.Register(server, cfg)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("kuso-mcp: server failed: %v", err)
	}
}
