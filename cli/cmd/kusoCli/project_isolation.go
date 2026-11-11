// kuso project isolation — read and switch a project's env isolation
// (the NetworkPolicies that keep previews, env groups and named envs off
// production pods and addons). Projects created before it existed are
// flat until an admin runs `enable`.

package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

var (
	isolationWait     time.Duration
	isolationProdOnly bool
	isolationYes      bool
)

var projectIsolationCmd = &cobra.Command{
	Use:   "isolation",
	Short: "Show or switch network isolation between a project's environments",
}

var projectIsolationStatusCmd = &cobra.Command{
	Use:   "status <project>",
	Short: "Show whether env isolation is on and what blocks turning it on",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		rep, err := getIsolation(args[0])
		if err != nil {
			return err
		}
		if outputFormat == "json" {
			return jsonOut(rep)
		}
		printIsolation(rep)
		return nil
	},
}

var projectIsolationEnableCmd = &cobra.Command{
	Use:   "enable <project>",
	Short: "Stamp env labels, wait for pods to roll, then turn env isolation on",
	Long: `Turn on env isolation for a project created before it was the default.

Three steps, all through the API:
  1. stamp: write the env label onto every env CR that lacks it. Each
     stamped env rolls its pods once (normal rolling update).
  2. wait until every env pod carries its label (--wait, default 5m).
  3. switch isolation on. From then on previews, env groups and named envs
     can't open connections to production pods, and env pods reach only the
     addons they subscribe to. --prod-only leaves non-production envs able
     to reach each other.

A pod that talks to an addon it does not subscribe to loses that
connection, so check subscriptions first ('kuso project addon list').`,
	Example: "  kuso project isolation enable shop\n  kuso project isolation enable shop --prod-only --yes",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project := args[0]
		rep, err := getIsolation(project)
		if err != nil {
			return err
		}
		if rep.Enabled && (rep.NonProductionEnvs || isolationProdOnly) {
			fmt.Printf("env isolation is already on for %s\n", project)
			return nil
		}
		if err := confirmDestructive(isolationYes, fmt.Sprintf(
			"Turn on env isolation for %s? Stamped envs roll their pods once.", project)); err != nil {
			return err
		}
		resp, err := api.StampEnvIsolation(project)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("stamp envs: %w", err)
		}
		var stamped struct {
			Stamped []string `json:"stamped"`
		}
		_ = json.Unmarshal(resp.Body(), &stamped)
		fmt.Printf("stamped %d env(s)\n", len(stamped.Stamped))

		deadline := time.Now().Add(isolationWait)
		for !rep.Ready {
			rep, err = getIsolation(project)
			if err != nil {
				return err
			}
			if rep.Ready {
				break
			}
			if len(rep.UnlabelledEnvs) > 0 {
				printIsolation(rep)
				return fmt.Errorf("%d env(s) have no env label and can't be stamped; label them by hand first", len(rep.UnlabelledEnvs))
			}
			if time.Now().After(deadline) {
				printIsolation(rep)
				return fmt.Errorf("pods still rolling after %s; re-run once they're done", isolationWait)
			}
			fmt.Fprintf(os.Stderr, "waiting: %d unstamped env(s), %d pod(s) without the env label\n",
				len(rep.UnstampedEnvs), len(rep.UnlabelledPods))
			time.Sleep(5 * time.Second)
		}

		nonProd := !isolationProdOnly
		resp, err = api.SetEnvIsolation(project, kusoApi.SetEnvIsolationRequest{Enabled: true, NonProductionEnvs: &nonProd})
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("enable: %w", err)
		}
		fmt.Printf("env isolation on for %s (non-production envs isolated from each other: %v)\n", project, nonProd)
		return nil
	},
}

var projectIsolationDisableCmd = &cobra.Command{
	Use:   "disable <project>",
	Short: "Turn env isolation off (every project pod can reach every other again)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(isolationYes, fmt.Sprintf(
			"Turn off env isolation for %s? Previews and staging can then reach production pods and addons.", args[0])); err != nil {
			return err
		}
		resp, err := api.SetEnvIsolation(args[0], kusoApi.SetEnvIsolationRequest{Enabled: false})
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("disable: %w", err)
		}
		fmt.Printf("env isolation off for %s\n", args[0])
		return nil
	},
}

func getIsolation(project string) (*kusoApi.EnvIsolationReport, error) {
	resp, err := api.GetEnvIsolation(project)
	if err := checkRespErr(resp, err); err != nil {
		return nil, fmt.Errorf("env isolation: %w", err)
	}
	var rep kusoApi.EnvIsolationReport
	if err := json.Unmarshal(resp.Body(), &rep); err != nil {
		return nil, fmt.Errorf("decode env isolation: %w", err)
	}
	return &rep, nil
}

func printIsolation(rep *kusoApi.EnvIsolationReport) {
	state := "off"
	switch {
	case rep.Enabled && rep.NonProductionEnvs:
		state = "on"
	case rep.Enabled:
		state = "on (production only; non-production envs can reach each other)"
	}
	fmt.Printf("project   %s\nisolation %s\nready     %v\n", rep.Project, state, rep.Ready)
	for _, l := range []struct {
		label string
		items []string
	}{
		{"unstamped envs", rep.UnstampedEnvs},
		{"unlabelled envs", rep.UnlabelledEnvs},
		{"unlabelled pods", rep.UnlabelledPods},
	} {
		if len(l.items) > 0 {
			fmt.Printf("%s: %v\n", l.label, l.items)
		}
	}
	if !rep.Enabled {
		fmt.Printf("\nturn it on with: kuso project isolation enable %s\n", rep.Project)
	}
}

func init() {
	projectCmd.AddCommand(projectIsolationCmd)
	projectIsolationCmd.AddCommand(projectIsolationStatusCmd, projectIsolationEnableCmd, projectIsolationDisableCmd)
	projectIsolationStatusCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	projectIsolationEnableCmd.Flags().DurationVar(&isolationWait, "wait", 5*time.Minute, "how long to wait for stamped envs' pods to roll")
	projectIsolationEnableCmd.Flags().BoolVar(&isolationProdOnly, "prod-only", false, "isolate envs from production only, not from each other")
	for _, c := range []*cobra.Command{projectIsolationEnableCmd, projectIsolationDisableCmd} {
		c.Flags().BoolVarP(&isolationYes, "yes", "y", false, "skip the confirmation prompt")
	}
}
