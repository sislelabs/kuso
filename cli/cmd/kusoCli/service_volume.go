// service_volume.go: `kuso service volume list|add|rm` — persistent disks
// mounted into a service's pods. The server only takes the whole volume
// list, so add/rm read the current list, change one entry and send it all.

package kusoCli

import (
	"fmt"
	"os"
	"strconv"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

var (
	serviceVolumeMount        string
	serviceVolumeSize         int
	serviceVolumeStorageClass string
	serviceVolumeAccessMode   string
	serviceVolumeRmYes        bool
	serviceVolumeOutput       string
)

func newServiceVolumeCmd() *cobra.Command {
	group := &cobra.Command{
		Use:     "volume",
		Aliases: []string{"volumes"},
		Short:   "Manage a service's persistent volumes",
	}

	list := &cobra.Command{
		Use:     "list <project> <service>",
		Aliases: []string{"ls"},
		Short:   "List a service's volumes",
		Example: `  kuso service volume list shop api
  kuso service volume list shop api -o json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			cur, err := fetchServiceSpec(args[0], args[1])
			if err != nil {
				return err
			}
			vols := cur.Spec.Volumes
			if vols == nil {
				vols = []kusoApi.VolumePatch{}
			}
			if serviceVolumeOutput == "json" {
				return jsonOut(vols)
			}
			if len(vols) == 0 {
				fmt.Printf("service %s/%s has no volumes\n", args[0], args[1])
				return nil
			}
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"NAME", "MOUNT", "SIZE", "STORAGE CLASS", "ACCESS MODE"})
			for _, v := range vols {
				size := "default"
				if v.SizeGi > 0 {
					size = strconv.Itoa(v.SizeGi) + "Gi"
				}
				t.Append([]string{v.Name, v.MountPath, size, orDash(v.StorageClass), orDash(v.AccessMode)})
			}
			t.Render()
			return nil
		},
	}
	list.Flags().StringVarP(&serviceVolumeOutput, "output", "o", "table", "output format [table, json]")

	add := &cobra.Command{
		Use:   "add <project> <service> <name>",
		Short: "Add a persistent volume to a service (redeploys it)",
		Long: `Add a persistent volume. kuso creates a PVC and mounts it at --mount in
every pod of the service; the service redeploys.

A ReadWriteOnce volume (the default) makes every later deploy stop the old
pod before starting the new one, so each deploy has a brief outage.
storage class and access mode can't change once the volume exists.`,
		Example: `  kuso service volume add shop api uploads --mount /app/uploads --size 5
  kuso service volume add shop api cache --mount /cache --size 10 --storage-class longhorn --access-mode rwx`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			cur, err := fetchServiceSpec(args[0], args[1])
			if err != nil {
				return err
			}
			vols, err := addVolume(cur.Spec.Volumes, kusoApi.VolumePatch{
				Name:         args[2],
				MountPath:    serviceVolumeMount,
				SizeGi:       serviceVolumeSize,
				StorageClass: serviceVolumeStorageClass,
				AccessMode:   serviceVolumeAccessMode,
			})
			if err != nil {
				return err
			}
			resp, err := api.PatchService(args[0], args[1], kusoApi.PatchServiceRequest{Volumes: &vols})
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("add volume: %w", err)
			}
			fmt.Printf("volume %s mounted at %s on %s/%s\n", args[2], serviceVolumeMount, args[0], args[1])
			return nil
		},
	}
	add.Flags().StringVar(&serviceVolumeMount, "mount", "", "absolute mount path in the container (required)")
	add.Flags().IntVar(&serviceVolumeSize, "size", 0, "size in Gi (0 = the chart default)")
	add.Flags().StringVar(&serviceVolumeStorageClass, "storage-class", "", "storage class (default: the cluster default; see 'kuso api GET kubernetes/storageclasses')")
	add.Flags().StringVar(&serviceVolumeAccessMode, "access-mode", "", "rwo|rwx|rox|ReadWriteOncePod (default rwo)")
	_ = add.MarkFlagRequired("mount")

	rm := &cobra.Command{
		Use:     "rm <project> <service> <name>",
		Aliases: []string{"remove", "delete"},
		Short:   "Unmount a volume from a service (the PVC and its data are kept)",
		Long: `Remove a volume from the service spec. Pods stop mounting it after the
redeploy. The PVC is NOT deleted: the data stays on disk with no pod
using it, and adding a volume with the same name later mounts it again.

Prompts for confirmation unless --yes.`,
		Example: `  kuso service volume rm shop api uploads
  kuso service volume rm shop api uploads --yes`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			cur, err := fetchServiceSpec(args[0], args[1])
			if err != nil {
				return err
			}
			vols, err := removeVolume(cur.Spec.Volumes, args[2])
			if err != nil {
				return err
			}
			if err := confirmDestructive(serviceVolumeRmYes, fmt.Sprintf(
				"Unmount volume %s from %s/%s? The app loses access to its data (the PVC is kept).", args[2], args[0], args[1])); err != nil {
				return err
			}
			resp, err := api.PatchService(args[0], args[1], kusoApi.PatchServiceRequest{Volumes: &vols})
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("remove volume: %w", err)
			}
			fmt.Printf("volume %s removed from %s/%s (PVC kept)\n", args[2], args[0], args[1])
			return nil
		},
	}
	rm.Flags().BoolVarP(&serviceVolumeRmYes, "yes", "y", false, "skip the confirmation prompt")

	group.AddCommand(list, add, rm)
	return group
}

// addVolume appends v, refusing a name or mount path that's already used
// (the server would reject both, less readably).
func addVolume(cur []kusoApi.VolumePatch, v kusoApi.VolumePatch) ([]kusoApi.VolumePatch, error) {
	for _, c := range cur {
		if c.Name == v.Name {
			return nil, fmt.Errorf("volume %q already exists (mounted at %s)", v.Name, c.MountPath)
		}
		if c.MountPath == v.MountPath {
			return nil, fmt.Errorf("mount path %s is already used by volume %q", v.MountPath, c.Name)
		}
	}
	out := append([]kusoApi.VolumePatch{}, cur...)
	return append(out, v), nil
}

func removeVolume(cur []kusoApi.VolumePatch, name string) ([]kusoApi.VolumePatch, error) {
	out := make([]kusoApi.VolumePatch, 0, len(cur))
	for _, c := range cur {
		if c.Name != name {
			out = append(out, c)
		}
	}
	if len(out) == len(cur) {
		return nil, fmt.Errorf("volume %q not found", name)
	}
	return out, nil
}

func init() {
	projectServiceCmd.AddCommand(newServiceVolumeCmd())
	serviceCmd.AddCommand(newServiceVolumeCmd())
}
