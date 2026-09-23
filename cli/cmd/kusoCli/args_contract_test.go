package kusoCli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// runRoot executes the real command tree with args and reports the error.
// Stdout/stderr are captured so help text doesn't pollute test output.
func runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	enforceArgContracts(rootCmd)
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	_, err := rootCmd.ExecuteC()
	return buf.String(), err
}

func TestUnknownSubcommandUnderGroupErrors(t *testing.T) {
	for _, args := range [][]string{
		{"get", "bogusxyz"},
		{"project", "bogusxyz"},
		{"node", "bogusxyz"},
	} {
		_, err := runRoot(t, args...)
		if err == nil {
			t.Errorf("kuso %s: want error, got exit 0", strings.Join(args, " "))
			continue
		}
		if !strings.Contains(err.Error(), "bogusxyz") {
			t.Errorf("kuso %s: error should name the bad token, got %q", strings.Join(args, " "), err)
		}
	}
}

func TestBareGroupStillPrintsHelp(t *testing.T) {
	out, err := runRoot(t, "get")
	if err != nil {
		t.Fatalf("bare `kuso get` must keep exiting 0, got %v", err)
	}
	if !strings.Contains(out, "Available Commands") {
		t.Errorf("bare `kuso get` should print help, got:\n%s", out)
	}
}

// Every runnable command must declare how many positionals it takes;
// cobra's default for a subcommand is "accept anything", which is how
// `kuso backup bogusxyz` started a real control-plane dump.
func TestEveryRunnableCommandDeclaresArgs(t *testing.T) {
	// cobra's built-in `help` takes a command path as positionals.
	enforceArgContracts(rootCmd)
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Runnable() && c.Args == nil && c != rootCmd && c.Name() != "help" {
			t.Errorf("%s: runnable with no Args validator", c.CommandPath())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
}

func TestLeafRejectsStrayPositional(t *testing.T) {
	for _, args := range [][]string{
		{"backup", "bogusxyz"},
		{"node", "cleanup", "server2"},
		{"status", "a", "b"},
	} {
		_, err := runRoot(t, args...)
		if err == nil || !(strings.Contains(err.Error(), "unknown command") || strings.Contains(err.Error(), "accepts at most")) {
			t.Errorf("kuso %s: want an args-validation error, got %v", strings.Join(args, " "), err)
		}
	}
}
