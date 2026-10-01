package kusoCli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Many commands inherit a persistent -o from their group, so they
// accepted -o json and printed text anyway, while JSON-only views
// accepted -o table and printed JSON. Commands are tagged here and
// enforceOutputContracts rejects a format they can't honour.
const (
	outputAnnotation = "kuso.output"
	outputJSONOnly   = "json"
	outputTextOnly   = "none"
)

func markOutput(kind string, cmds ...*cobra.Command) {
	for _, c := range cmds {
		if c.Annotations == nil {
			c.Annotations = map[string]string{}
		}
		c.Annotations[outputAnnotation] = kind
	}
}

func init() {
	markOutput(outputTextOnly,
		envSetCmd, envUnsetCmd, envShareCmd, envUnshareCmd,
		secretSetCmd, secretUnsetCmd,
		roleCreateCmd, roleEditCmd, roleDeleteCmd,
		githubStatusCmd, githubRefreshCmd, githubConfigureCmd,
	)
	markOutput(outputJSONOnly,
		instanceConfigGetCmd, buildSettingsGetCmd, sessionSettingsGetCmd,
		githubCheckRepoCmd, githubDetectRuntimeCmd, githubScanAddonsCmd,
		inviteLookupCmd, userProfileCmd, revisionShowCmd,
	)
}

// checkOutputContract errors when -o was passed explicitly with a value
// the command can't produce.
func checkOutputContract(cmd *cobra.Command) error {
	kind := cmd.Annotations[outputAnnotation]
	if kind == "" {
		return nil
	}
	f := cmd.Flags().Lookup("output")
	if f == nil || !f.Changed {
		return nil
	}
	v := f.Value.String()
	switch kind {
	case outputJSONOnly:
		if v != "json" {
			return fmt.Errorf("%s only prints JSON; -o %s is not supported", cmd.CommandPath(), v)
		}
	case outputTextOnly:
		return fmt.Errorf("%s has no machine-readable output; drop -o %s", cmd.CommandPath(), v)
	}
	return nil
}

func enforceOutputContracts(root *cobra.Command) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Annotations[outputAnnotation] != "" && c.PreRunE == nil {
			c.PreRunE = func(cmd *cobra.Command, _ []string) error { return checkOutputContract(cmd) }
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}
