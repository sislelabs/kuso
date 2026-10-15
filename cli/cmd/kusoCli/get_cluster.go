// get_cluster.go — cluster-wide reads the web UI's pickers use:
//
//	kuso get storageclasses [-o json]   volume storage-class picker
//	kuso get domains [-o json]          every Ingress host ("host taken" hint)
//
// Both are admin-only and cached ~30s server-side.

package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

var getStorageClassesCmd = &cobra.Command{
	Use:     "storageclasses",
	Aliases: []string{"storageclass", "sc"},
	Short:   "List the cluster's storage classes (admin)",
	Long: `List the cluster's storage classes: the values a service volume's
storageClass accepts. The default class is used when a volume sets none.`,
	Example: `  kuso get storageclasses
  kuso get sc -o json | jq -r '.[].metadata.name'`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.StorageClasses()
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("storage classes: %w", err)
		}
		var raw []map[string]any
		if err := json.Unmarshal(resp.Body(), &raw); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		switch outputFormat {
		case "json":
			if raw == nil {
				raw = []map[string]any{}
			}
			return jsonOut(raw)
		case "table", "":
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
		var scs []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Provisioner          string `json:"provisioner"`
			ReclaimPolicy        string `json:"reclaimPolicy"`
			VolumeBindingMode    string `json:"volumeBindingMode"`
			AllowVolumeExpansion *bool  `json:"allowVolumeExpansion"`
		}
		if err := json.Unmarshal(resp.Body(), &scs); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if len(scs) == 0 {
			fmt.Println("no storage classes")
			return nil
		}
		t := tablewriter.NewWriter(os.Stdout)
		t.SetHeader([]string{"NAME", "DEFAULT", "PROVISIONER", "RECLAIM", "BINDING", "EXPANDABLE"})
		for _, sc := range scs {
			def := ""
			if sc.Metadata.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
				def = "yes"
			}
			expand := "no"
			if sc.AllowVolumeExpansion != nil && *sc.AllowVolumeExpansion {
				expand = "yes"
			}
			t.Append([]string{sc.Metadata.Name, def, sc.Provisioner, sc.ReclaimPolicy, sc.VolumeBindingMode, expand})
		}
		t.Render()
		return nil
	},
}

var getDomainsCmd = &cobra.Command{
	Use:   "domains",
	Short: "List every hostname routed by an Ingress in the cluster (admin)",
	Long: `List every Ingress host in the cluster, across all projects. Use it
to check whether a hostname is already taken before adding it to a
service.`,
	Example: `  kuso get domains
  kuso get domains -o json | jq -r '.[]' | grep example.com`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.IngressDomains()
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("domains: %w", err)
		}
		var hosts []string
		if err := json.Unmarshal(resp.Body(), &hosts); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if hosts == nil {
			hosts = []string{}
		}
		switch outputFormat {
		case "json":
			return jsonOut(hosts)
		case "table", "":
			for _, h := range hosts {
				fmt.Println(h)
			}
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

func init() {
	getCmd.AddCommand(getStorageClassesCmd, getDomainsCmd)
}
