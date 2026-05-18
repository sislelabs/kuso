package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso instance-pg` — manage the cluster-shared Postgres. First-
// class story: one Postgres serves every project that opts in, with
// kuso carving per-project databases inside it.
//
// Two provisioning models:
//   * managed — kuso runs Postgres on this cluster via the kusoaddon
//     chart. `kuso instance-pg provision`.
//   * external — admin points at an off-cluster PG (Neon, RDS,
//     anywhere). `kuso instance-pg external <DSN>`.
//
// Once registered, per-project DBs are carved out via
// `kuso project addon add … --use-instance-addon pg`.

var instancePGCmd = &cobra.Command{
	Use:     "instance-pg",
	Aliases: []string{"ipg", "cluster-db"},
	Short:   "Manage the cluster-shared Postgres (admin only)",
}

// instancePGStatusResp mirrors the server's Status type. Inline here
// (not imported from server-go) to keep the CLI binary's
// import surface tight — server-go is a separate module.
type instancePGStatusResp struct {
	Mode          string `json:"mode"`
	Phase         string `json:"phase,omitempty"`
	Host          string `json:"host,omitempty"`
	Port          string `json:"port,omitempty"`
	User          string `json:"user,omitempty"`
	Version       string `json:"version,omitempty"`
	Size          string `json:"size,omitempty"`
	HA            bool   `json:"ha,omitempty"`
	StorageSize   string `json:"storageSize,omitempty"`
	ProjectsUsing int    `json:"projectsUsing"`
	LastError     string `json:"lastError,omitempty"`
}

var instancePGStatusCmd = &cobra.Command{
	Use:     "status",
	Aliases: []string{"get"},
	Short:   "Show cluster Postgres status (mode, phase, consumers)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		st, err := readInstancePGStatus()
		if err != nil {
			return err
		}
		if outputFormat == "json" {
			return jsonOut(st)
		}
		printInstancePGStatus(st)
		return nil
	},
}

var (
	provisionSize        string
	provisionVersion     string
	provisionStorageSize string
	provisionHA          bool
	provisionWait        bool
)

var instancePGProvisionCmd = &cobra.Command{
	Use:   "provision",
	Short: "Provision an on-cluster Postgres via the kusoaddon chart",
	Long: `Provisions a Postgres StatefulSet in the kuso namespace using the
existing kusoaddon helm chart. Returns immediately — the helm-
operator installs the chart asynchronously (30-90s). Pass --wait
to block until the PG is Ready and the admin DSN has been
registered into instance-secrets.

Once Ready, per-project DBs are provisioned via:
    kuso project addon add <project> <name> postgres --use-instance-addon pg

Refuses when a managed PG already exists or an external DSN is
configured — disable the existing one first.`,
	Example: `  kuso instance-pg provision
  kuso instance-pg provision --size medium --storage 50Gi --wait
  kuso instance-pg provision --ha`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		req := kusoApi.ProvisionInstancePGRequest{
			Size:        provisionSize,
			HA:          provisionHA,
			Version:     provisionVersion,
			StorageSize: provisionStorageSize,
		}
		resp, err := api.ProvisionInstancePG(req)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Println("provisioning started — kuso will install Postgres on the cluster")
		if !provisionWait {
			fmt.Println("(use --wait to block until ready, or `kuso instance-pg status` to poll)")
			return nil
		}
		return waitForReady(120 * time.Second)
	},
}

var externalDSN string

var instancePGExternalCmd = &cobra.Command{
	Use:   "external <DSN>",
	Short: "Register an external Postgres by DSN",
	Long: `Validates the DSN by connecting + SELECT 1, then stores it as the
cluster-shared admin DSN. The user must have CREATEDB + CREATEROLE
privileges so kuso can carve per-project databases.

Refuses when an on-cluster managed PG already exists — run
'kuso instance-pg disable' first to switch modes.`,
	Example: `  kuso instance-pg external 'postgres://admin:pw@db.example.com:5432/postgres?sslmode=disable'`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ConfigureExternalInstancePG(kusoApi.ConfigureExternalInstancePGRequest{DSN: args[0]})
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Println("external Postgres registered — projects can now use --use-instance-addon pg")
		return nil
	},
}

var instancePGDisableCmd = &cobra.Command{
	Use:     "disable",
	Aliases: []string{"down", "rm"},
	Short:   "Disable the cluster Postgres (refuses while consumers exist)",
	Long: `Tears down whichever mode is active:
  * managed mode  — deletes the on-cluster StatefulSet. ALL DATA LOST.
  * external mode — just disconnects; the remote PG is untouched.

Refuses with a non-zero exit when any project still has an addon
with spec.useInstanceAddon = "pg". The error lists the offending
projects so you know what to migrate before tearing down.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.DisableInstancePG()
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Println("cluster Postgres disabled")
		return nil
	},
}

// readInstancePGStatus is shared by `status` + the --wait poll loop.
func readInstancePGStatus() (*instancePGStatusResp, error) {
	resp, err := api.GetInstancePG()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode() >= 300 {
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
	}
	var st instancePGStatusResp
	if err := json.Unmarshal(resp.Body(), &st); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &st, nil
}

// printInstancePGStatus renders the status as a tight, scannable
// block. Avoids tablewriter — there's only one row of data, a
// horizontal table just wastes vertical space.
func printInstancePGStatus(st *instancePGStatusResp) {
	switch st.Mode {
	case "none":
		fmt.Println("mode: not configured")
		fmt.Println("→ kuso instance-pg provision       # run Postgres on this cluster")
		fmt.Println("→ kuso instance-pg external <DSN>  # point at an external Postgres")
		return
	case "managed":
		fmt.Printf("mode:              managed (on-cluster)\n")
		fmt.Printf("phase:             %s\n", st.Phase)
	case "external":
		fmt.Printf("mode:              external\n")
		fmt.Printf("phase:             ready\n")
	}
	if st.Host != "" {
		hostport := st.Host
		if st.Port != "" {
			hostport += ":" + st.Port
		}
		fmt.Printf("host:              %s\n", hostport)
	}
	if st.User != "" {
		fmt.Printf("user:              %s\n", st.User)
	}
	if st.Version != "" {
		fmt.Printf("version:           %s\n", st.Version)
	}
	if st.Size != "" {
		fmt.Printf("size:              %s\n", st.Size)
	}
	if st.StorageSize != "" {
		fmt.Printf("storage:           %s\n", st.StorageSize)
	}
	if st.Mode == "managed" {
		fmt.Printf("ha:                %v\n", st.HA)
	}
	fmt.Printf("projects connected: %d\n", st.ProjectsUsing)
	if st.LastError != "" {
		fmt.Printf("last error:        %s\n", st.LastError)
	}
}

// waitForReady polls /api/instance-pg every 3s until phase=ready,
// the request errors, or `timeout` elapses. Prints a single status
// line per poll so the operator can see progress without spam.
func waitForReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	lastPhase := ""
	for time.Now().Before(deadline) {
		st, err := readInstancePGStatus()
		if err != nil {
			return err
		}
		if st.Phase != lastPhase {
			fmt.Fprintf(os.Stderr, "  → phase: %s\n", st.Phase)
			lastPhase = st.Phase
		}
		switch st.Phase {
		case "ready":
			fmt.Println("ready ✓")
			printInstancePGStatus(st)
			return nil
		case "failed":
			if st.LastError != "" {
				return fmt.Errorf("provisioning failed: %s", st.LastError)
			}
			return fmt.Errorf("provisioning failed (see `kuso instance-pg status` for details)")
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("timed out waiting for ready after %s; use `kuso instance-pg status` to check", timeout)
}

func init() {
	rootCmd.AddCommand(instancePGCmd)
	instancePGCmd.AddCommand(instancePGStatusCmd)
	instancePGStatusCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")

	instancePGCmd.AddCommand(instancePGProvisionCmd)
	instancePGProvisionCmd.Flags().StringVar(&provisionSize, "size", "", "instance size [small|medium|large]")
	instancePGProvisionCmd.Flags().StringVar(&provisionVersion, "version", "", "Postgres major version (default 16)")
	instancePGProvisionCmd.Flags().StringVar(&provisionStorageSize, "storage", "", "PVC size (default 20Gi)")
	instancePGProvisionCmd.Flags().BoolVar(&provisionHA, "ha", false, "use CloudNativePG with 3 replicas (requires CNPG operator)")
	instancePGProvisionCmd.Flags().BoolVar(&provisionWait, "wait", false, "block until the Postgres is Ready and the admin DSN is registered")

	_ = externalDSN // reserved for future flag-based external (currently positional)
	instancePGCmd.AddCommand(instancePGExternalCmd)

	instancePGCmd.AddCommand(instancePGDisableCmd)
}
