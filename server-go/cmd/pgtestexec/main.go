// Command pgtestexec is a `go test -exec` wrapper that gives every test
// binary (one per package) its own Postgres database, so the packages whose
// tests truncate shared tables can run in parallel instead of under -p 1.
//
//	go build -o /tmp/pgtestexec ./cmd/pgtestexec
//	KUSO_TEST_PG_DSN=postgres://... go test -race -exec /tmp/pgtestexec ./...
//
// With KUSO_TEST_PG_DSN set it creates <db>_<pid>_<n> on the same server
// (a superuser DSN), points KUSO_TEST_PG_DSN at it for the child, and drops
// it afterwards. Without the DSN it just runs the binary. Test-only: nothing
// in the server depends on it.
package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lib/pq"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: pgtestexec <test binary> [args...]")
		os.Exit(2)
	}
	dsn := os.Getenv("KUSO_TEST_PG_DSN")
	if dsn == "" {
		os.Exit(run(os.Args[1:], nil))
	}
	child, drop, err := createDB(dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgtestexec: %v\n", err)
		os.Exit(1)
	}
	code := run(os.Args[1:], []string{"KUSO_TEST_PG_DSN=" + child})
	if err := drop(); err != nil {
		fmt.Fprintf(os.Stderr, "pgtestexec: %v\n", err)
	}
	os.Exit(code)
}

func createDB(dsn string) (string, func() error, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", nil, fmt.Errorf("parse KUSO_TEST_PG_DSN: %w", err)
	}
	parent := strings.TrimPrefix(u.Path, "/")
	if parent == "" {
		parent = "kuso_test"
	}
	var b [3]byte
	_, _ = rand.Read(b[:])
	name := fmt.Sprintf("%s_%d_%s", parent, os.Getpid(), hex.EncodeToString(b[:]))
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		return "", nil, err
	}
	if _, err := admin.Exec(`CREATE DATABASE ` + pq.QuoteIdentifier(name)); err != nil {
		_ = admin.Close()
		return "", nil, fmt.Errorf("create database %s: %w", name, err)
	}
	u.Path = "/" + name
	drop := func() error {
		defer admin.Close()
		if _, err := admin.Exec(`DROP DATABASE IF EXISTS ` + pq.QuoteIdentifier(name) + ` WITH (FORCE)`); err != nil {
			return fmt.Errorf("drop database %s: %w", name, err)
		}
		return nil
	}
	return u.String(), drop, nil
}

// run executes the test binary with extra env and returns its exit code.
// SIGINT/SIGTERM are forwarded to the child rather than killing the wrapper,
// so it can still drop the database once the child exits.
func run(argv, env []string) int {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), env...)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "pgtestexec: %v\n", err)
		return 1
	}
	go func() {
		for s := range sigs {
			_ = cmd.Process.Signal(s)
		}
	}()
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				return 128 + int(ws.Signal())
			}
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "pgtestexec: %v\n", err)
		return 1
	}
	return 0
}
