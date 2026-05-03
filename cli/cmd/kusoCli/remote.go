package kusoCli

// `kuso remote` family — manage multiple kuso server entries in
// ~/.kuso/kuso.yaml. A "remote" here = an Instance.
//
//   kuso remote                 list configured instances
//   kuso remote create          interactive wizard for a new instance
//   kuso remote select <name>   switch which instance commands target
//   kuso remote delete <name>   remove an instance from local config

import (
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

var remoteCmd = &cobra.Command{
	Use:     "remote",
	Aliases: []string{"r", "remotes"},
	Short:   "Manage configured kuso instances",
	Run: func(cmd *cobra.Command, args []string) {
		printRemotes()
	},
}

var remoteCreateCmd = &cobra.Command{
	Use:     "create",
	Aliases: []string{"new", "add"},
	Short:   "Add a new kuso instance to the local config",
	RunE: func(cmd *cobra.Command, args []string) error {
		return createRemote()
	},
}

var remoteSelectCmd = &cobra.Command{
	Use:     "select [name]",
	Aliases: []string{"use"},
	Args:    cobra.MaximumNArgs(1),
	Short:   "Switch the active kuso instance",
	RunE: func(cmd *cobra.Command, args []string) error {
		name := ""
		if len(args) == 1 {
			name = args[0]
		}
		if name == "" {
			name = selectFromList("Pick an instance", instanceNameList, currentInstanceName)
		}
		if name == "" {
			return fmt.Errorf("no instance selected")
		}
		return selectRemote(name)
	},
}

var remoteDeleteCmd = &cobra.Command{
	Use:     "delete [name]",
	Aliases: []string{"rm", "del"},
	Args:    cobra.MaximumNArgs(1),
	Short:   "Remove an instance from the local config",
	RunE: func(cmd *cobra.Command, args []string) error {
		name := ""
		if len(args) == 1 {
			name = args[0]
		}
		if name == "" {
			name = selectFromList("Pick an instance to delete", instanceNameList, "")
		}
		if name == "" {
			return fmt.Errorf("no instance selected")
		}
		return deleteRemote(name)
	},
}

func init() {
	rootCmd.AddCommand(remoteCmd)
	remoteCmd.AddCommand(remoteCreateCmd, remoteSelectCmd, remoteDeleteCmd)
}

// printRemotes renders the instance table. Active + auth columns use
// a checkmark glyph (more compact than text in a column header).
func printRemotes() {
	t := tablewriter.NewWriter(os.Stdout)
	t.SetHeader([]string{"Active", "Auth", "Name", "API URL", "Config"})
	t.SetAutoWrapText(false)
	t.SetAutoFormatHeaders(true)
	t.SetBorder(false)
	t.SetCenterSeparator("")
	t.SetRowSeparator("")
	t.SetTablePadding("\t")
	t.SetNoWhiteSpace(true)
	for _, name := range instanceNameList {
		active := ""
		if name == currentInstanceName {
			active = "✔"
		}
		auth := ""
		if credentialsConfig.GetString(name) != "" {
			auth = "✔"
		}
		t.Append([]string{active, auth, name, instanceList[name].ApiUrl, instanceList[name].ConfigPath})
	}
	t.Render()
}

// createRemote runs the interactive wizard for a fresh instance.
// Asked: name + API URL. The instance is added and made current; the
// user is then expected to run `kuso login` to mint a token.
func createRemote() error {
	name := promptLine("Instance name", "(unique, e.g. prod, staging)", "")
	if name == "" {
		return fmt.Errorf("instance name is required")
	}
	apiURL := promptLine("API URL", "(e.g. https://kuso.example.com)", "")
	if apiURL == "" {
		return fmt.Errorf("API URL is required")
	}

	if instanceList == nil {
		instanceList = map[string]Instance{}
	}
	cfgPath := defaultConfigPath()
	instanceList[name] = Instance{
		Name:       name,
		ApiUrl:     apiURL,
		IacBaseDir: ".kuso",
		ConfigPath: cfgPath,
	}
	if !contains(instanceNameList, name) {
		instanceNameList = append(instanceNameList, name)
	}
	currentInstanceName = name
	currentInstance = instanceList[name]
	if err := saveInstances(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	api.SetApiUrl(apiURL, "")
	fmt.Println("Added instance:", name, "→", apiURL)
	fmt.Println("Run `kuso login` to authenticate.")
	return nil
}

// selectRemote switches the active instance and re-points the API
// client at it. We swap the bearer token at the same time so the next
// request is authenticated against the chosen instance.
func selectRemote(name string) error {
	inst, ok := instanceList[name]
	if !ok {
		return fmt.Errorf("instance %q not found", name)
	}
	currentInstanceName = name
	currentInstance = inst
	if err := saveInstances(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	api.SetApiUrl(inst.ApiUrl, credentialsConfig.GetString(name))
	fmt.Println("Switched to:", name)
	return nil
}

// deleteRemote removes an instance from kuso.yaml. We DON'T touch the
// credentials file — the user may want to re-add the same instance
// later, and re-typing a JWT is annoying.
func deleteRemote(name string) error {
	if _, ok := instanceList[name]; !ok {
		return fmt.Errorf("instance %q not found", name)
	}
	delete(instanceList, name)
	out := instanceNameList[:0]
	for _, n := range instanceNameList {
		if n != name {
			out = append(out, n)
		}
	}
	instanceNameList = out
	if currentInstanceName == name {
		currentInstanceName = ""
		currentInstance = Instance{}
	}
	if err := saveInstances(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	fmt.Println("Deleted instance:", name)
	return nil
}

