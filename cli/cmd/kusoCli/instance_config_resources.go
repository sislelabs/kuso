// `kuso instance-config` sub-resources — pod-size presets (full CRUD),
// runpacks (list/delete; no create route exists), and the read-only
// config views (templates, banner, clusterissuer, registry). All are
// admin-gated server-side.
//
//   kuso instance-config podsize list [-o json]
//   kuso instance-config podsize create --name small --cpu-limit 500m --mem-limit 512Mi --cpu-request 100m --mem-request 128Mi
//   kuso instance-config podsize edit <id> --name ...
//   kuso instance-config podsize delete <id>
//   kuso instance-config runpack list [-o json]
//   kuso instance-config runpack delete <id>
//   kuso instance-config templates|banner|clusterissuer|registry [-o json]

package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"kuso/pkg/kusoApi"

	"github.com/go-resty/resty/v2"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

var (
	podSizeName       string
	podSizeCPULimit   string
	podSizeMemLimit   string
	podSizeCPUReq     string
	podSizeMemReq     string
	podSizeDescr      string
)

var instanceConfigPodSizeCmd = &cobra.Command{
	Use:     "podsize",
	Aliases: []string{"podsizes", "pod-size"},
	Short:   "Manage pod-size presets (admin)",
}

var instanceConfigPodSizeListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List pod-size presets",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListPodSizes()
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list pod sizes: %w", err)
		}
		var sizes []map[string]any
		if err := json.Unmarshal(resp.Body(), &sizes); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		sort.Slice(sizes, func(i, j int) bool {
			return asString(sizes[i]["Name"]) < asString(sizes[j]["Name"])
		})
		switch outputFormat {
		case "json":
			return jsonOut(sizes)
		case "table", "":
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"ID", "NAME", "CPU REQ/LIM", "MEM REQ/LIM", "DESCRIPTION"})
			for _, s := range sizes {
				t.Append([]string{
					asString(s["ID"]),
					asString(s["Name"]),
					asString(s["CPURequest"]) + " / " + asString(s["CPULimit"]),
					asString(s["MemoryRequest"]) + " / " + asString(s["MemoryLimit"]),
					nullStringText(s["Description"]),
				})
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

// nullStringText renders a JSON-decoded sql.NullString ({String,Valid})
// as its plain value, or "-" when null. Accepts already-flat strings too.
func nullStringText(v any) string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return "-"
		}
		return t
	case map[string]any:
		if valid, _ := t["Valid"].(bool); valid {
			return asString(t["String"])
		}
	}
	return "-"
}

func podSizeFromFlags() kusoApi.PodSize {
	return kusoApi.PodSize{
		Name:          podSizeName,
		CPULimit:      podSizeCPULimit,
		MemoryLimit:   podSizeMemLimit,
		CPURequest:    podSizeCPUReq,
		MemoryRequest: podSizeMemReq,
		Description:   kusoApi.NewNullString(podSizeDescr),
	}
}

var instanceConfigPodSizeCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a pod-size preset",
	Args:  cobra.NoArgs,
	Example: `  kuso instance-config podsize create --name small \
    --cpu-request 100m --cpu-limit 500m --mem-request 128Mi --mem-limit 512Mi`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if podSizeName == "" {
			return fmt.Errorf("--name is required")
		}
		resp, err := api.CreatePodSize(podSizeFromFlags())
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("create pod size: %w", err)
		}
		var data map[string]any
		_ = json.Unmarshal(resp.Body(), &data)
		fmt.Printf("pod size %q created (id=%s)\n", podSizeName, asString(data["ID"]))
		return nil
	},
}

var instanceConfigPodSizeEditCmd = &cobra.Command{
	Use:   "edit <id>",
	Short: "Update a pod-size preset (replaces all fields)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if podSizeName == "" {
			return fmt.Errorf("--name is required")
		}
		resp, err := api.UpdatePodSize(args[0], podSizeFromFlags())
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("update pod size: %w", err)
		}
		fmt.Printf("pod size %s updated\n", args[0])
		return nil
	},
}

var instanceConfigPodSizeDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Aliases: []string{"rm", "remove"},
	Short:   "Delete a pod-size preset",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.DeletePodSize(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("delete pod size: %w", err)
		}
		fmt.Printf("pod size %s deleted\n", args[0])
		return nil
	},
}

var instanceConfigRunpackCmd = &cobra.Command{
	Use:     "runpack",
	Aliases: []string{"runpacks"},
	Short:   "Inspect / remove runpacks (admin)",
}

var instanceConfigRunpackListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List runpacks",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListRunpacks()
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list runpacks: %w", err)
		}
		var packs []map[string]any
		if err := json.Unmarshal(resp.Body(), &packs); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(packs)
		case "table", "":
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"ID", "NAME", "LANGUAGE"})
			for _, p := range packs {
				t.Append([]string{asString(p["ID"]), asString(p["Name"]), asString(p["Language"])})
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

var instanceConfigRunpackDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Aliases: []string{"rm", "remove"},
	Short:   "Delete a runpack",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.DeleteRunpack(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("delete runpack: %w", err)
		}
		fmt.Printf("runpack %s deleted\n", args[0])
		return nil
	},
}

// simpleConfigReadCmd builds a read-only `instance-config <name>` command
// that dumps a config sub-resource as JSON.
func simpleConfigReadCmd(use, short string, fetch func() (*resty.Response, error)) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			resp, err := fetch()
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("%s: %w", use, err)
			}
			var v any
			if err := json.Unmarshal(resp.Body(), &v); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			return jsonOut(v)
		},
	}
}

func init() {
	instanceConfigCmd.AddCommand(instanceConfigPodSizeCmd)
	instanceConfigPodSizeCmd.AddCommand(instanceConfigPodSizeListCmd)
	instanceConfigPodSizeCmd.AddCommand(instanceConfigPodSizeCreateCmd)
	instanceConfigPodSizeCmd.AddCommand(instanceConfigPodSizeEditCmd)
	instanceConfigPodSizeCmd.AddCommand(instanceConfigPodSizeDeleteCmd)

	instanceConfigCmd.AddCommand(instanceConfigRunpackCmd)
	instanceConfigRunpackCmd.AddCommand(instanceConfigRunpackListCmd)
	instanceConfigRunpackCmd.AddCommand(instanceConfigRunpackDeleteCmd)

	for _, c := range []*cobra.Command{instanceConfigPodSizeCreateCmd, instanceConfigPodSizeEditCmd} {
		c.Flags().StringVar(&podSizeName, "name", "", "preset name")
		c.Flags().StringVar(&podSizeCPULimit, "cpu-limit", "", "CPU limit (e.g. 500m)")
		c.Flags().StringVar(&podSizeMemLimit, "mem-limit", "", "memory limit (e.g. 512Mi)")
		c.Flags().StringVar(&podSizeCPUReq, "cpu-request", "", "CPU request (e.g. 100m)")
		c.Flags().StringVar(&podSizeMemReq, "mem-request", "", "memory request (e.g. 128Mi)")
		c.Flags().StringVar(&podSizeDescr, "description", "", "description")
	}

	// Read-only config views.
	instanceConfigCmd.AddCommand(simpleConfigReadCmd("templates", "Show service templates", func() (*resty.Response, error) {
		return api.GetConfigTemplates()
	}))
	instanceConfigCmd.AddCommand(simpleConfigReadCmd("banner", "Show instance banner", func() (*resty.Response, error) {
		return api.GetConfigBanner()
	}))
	instanceConfigCmd.AddCommand(simpleConfigReadCmd("clusterissuer", "Show cert-manager cluster issuer", func() (*resty.Response, error) {
		return api.GetConfigClusterIssuer()
	}))
	instanceConfigCmd.AddCommand(simpleConfigReadCmd("registry", "Show image-registry config", func() (*resty.Response, error) {
		return api.GetConfigRegistry()
	}))

	instanceConfigCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
}
