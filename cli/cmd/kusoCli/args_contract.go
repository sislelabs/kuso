package kusoCli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// enforceArgContracts makes every pure group command (subcommands, no
// Run of its own) reject stray positionals. Cobra's default for a
// non-root group is to print help and exit 0 on `kuso get bogusxyz`,
// which scripts read as success. A bare `kuso get` still prints help.
func enforceArgContracts(root *cobra.Command) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c != root && c.HasSubCommands() && !c.Runnable() {
			c.Args = unknownSubcommand
			c.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
		} else if c.Runnable() && c.Args != nil && c.Annotations[argsUsageWrapped] == "" {
			c.Args = withUsageLine(c.Args)
			if c.Annotations == nil {
				c.Annotations = map[string]string{}
			}
			c.Annotations[argsUsageWrapped] = "1"
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	enforceOutputContracts(root)
}

func unknownSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if cmd.SuggestionsMinimumDistance <= 0 {
		cmd.SuggestionsMinimumDistance = 2
	}
	msg := fmt.Sprintf("unknown command %q for %q", args[0], cmd.CommandPath())
	if s := cmd.SuggestionsFor(args[0]); len(s) > 0 {
		msg += "\n\nDid you mean this?\n\t" + strings.Join(s, "\n\t")
	}
	return fmt.Errorf("%s\nRun '%s --help' for usage", msg, cmd.CommandPath())
}

const argsUsageWrapped = "kuso.args-usage-wrapped"

// withUsageLine appends the command's usage line to an arg-count error.
// Root has SilenceUsage set, so without this a wrong arg count prints only
// cobra's bare "accepts 2 arg(s), received 1".
func withUsageLine(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validate(cmd, args); err != nil {
			return fmt.Errorf("%w\nUsage: %s", err, cmd.UseLine())
		}
		return nil
	}
}
