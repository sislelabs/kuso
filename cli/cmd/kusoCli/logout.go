package kusoCli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var logoutCmd = &cobra.Command{
	Use:   "logout [instance]",
	Args:  cobra.MaximumNArgs(1),
	Short: "Remove the stored token for a kuso instance",
	Long: `Deletes the bearer token saved in ~/.kuso/credentials.yaml for the
given instance (default: the active one). The instance entry stays, so
'kuso login' re-authenticates it.

This is local only: the token is not revoked on the server. Revoke API
tokens from the web UI if the machine is no longer trusted.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := ""
		if len(args) == 1 {
			name = args[0]
		}
		return logout(name)
	},
}

func logout(name string) error {
	if name == "" {
		name = currentInstanceName
	}
	if name == "" {
		return fmt.Errorf("no instance selected — pass the instance name")
	}
	if _, ok := instanceList[name]; !ok {
		return fmt.Errorf("instance %q not found", name)
	}
	removed, err := deleteToken(name)
	if err != nil {
		return fmt.Errorf("remove token: %w", err)
	}
	if !removed {
		fmt.Println("No stored token for:", name)
		return nil
	}
	fmt.Println("Logged out of:", name)
	return nil
}

func init() {
	rootCmd.AddCommand(logoutCmd)
}
