// service_extra.go hangs the wake + drift commands off the
// `kuso project service` subtree (projectServiceCmd, defined in
// project.go). Kept in a separate file to avoid churning project.go.
//
//	kuso project service wake  <project> <service>
//	kuso project service drift <project> <service> [-o json]

package kusoCli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var serviceWakeCmd = &cobra.Command{
	Use:   "wake <project> <service>",
	Short: "Wake a slept (scale-to-zero) service back to its minimum replicas",
	Long: `Kick a service that has scaled to zero back up to its configured minimum
replicas. The wake is asynchronous — the pod comes up on the next reconcile.
No-op for a service that's already running.`,
	Example: `  kuso project service wake scubatony api`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.WakeService(args[0], args[1])
		if err != nil {
			return fmt.Errorf("wake service: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("wake requested for %s/%s\n", args[0], args[1])
		return nil
	},
}

var serviceDriftCmd = &cobra.Command{
	Use:   "drift <project> <service>",
	Short: "Show whether a service's running pods match its saved spec",
	Long: `Report drift between a service's saved spec, the production environment
CR, and the running pods:

  - specPending:    fields edited on the service CR but not yet propagated
                    to the production env CR.
  - rolloutPending: helm-operator hasn't observed the latest env generation.
  - podsStale:      env spec is propagated but the running pods still carry
                    the old template (mid-rollout or a rollout-blocking
                    crash/image-pull failure).

A clean report means the running pods match the saved spec.`,
	Example: `  kuso project service drift scubatony api
  kuso project service drift scubatony api -o json`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetServiceDrift(args[0], args[1])
		if err != nil {
			return fmt.Errorf("service drift: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}

		if outputFormat == "json" {
			fmt.Println(string(resp.Body()))
			return nil
		}

		var d struct {
			SpecPending      []string `json:"specPending"`
			RolloutPending   bool     `json:"rolloutPending"`
			PodsStale        []string `json:"podsStale"`
			LastRolloutAt    string   `json:"lastRolloutAt"`
			LastSpecMutation string   `json:"lastSpecMutation"`
			EnvName          string   `json:"envName"`
			HelmError        string   `json:"helmError"`
			HelmReleasePhase string   `json:"helmReleasePhase"`
		}
		if err := json.Unmarshal(resp.Body(), &d); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}

		inSync := len(d.SpecPending) == 0 && !d.RolloutPending && len(d.PodsStale) == 0
		if inSync {
			fmt.Printf("%s/%s: in sync — running pods match the saved spec\n", args[0], args[1])
		} else {
			fmt.Printf("%s/%s: DRIFTED\n", args[0], args[1])
		}
		if d.EnvName != "" {
			fmt.Printf("  env:              %s\n", d.EnvName)
		}
		if len(d.SpecPending) > 0 {
			fmt.Printf("  specPending:      %v\n", d.SpecPending)
		}
		fmt.Printf("  rolloutPending:   %v\n", d.RolloutPending)
		if len(d.PodsStale) > 0 {
			fmt.Printf("  podsStale:        %v\n", d.PodsStale)
		}
		if d.HelmReleasePhase != "" {
			fmt.Printf("  helmReleasePhase: %s\n", d.HelmReleasePhase)
		}
		if d.HelmError != "" {
			fmt.Printf("  helmError:        %s\n", d.HelmError)
		}
		if d.LastSpecMutation != "" {
			fmt.Printf("  lastSpecMutation: %s\n", d.LastSpecMutation)
		}
		if d.LastRolloutAt != "" {
			fmt.Printf("  lastRolloutAt:    %s\n", d.LastRolloutAt)
		}
		return nil
	},
}

func init() {
	// projectServiceCmd is the package-level var defined in project.go.
	projectServiceCmd.AddCommand(serviceWakeCmd)
	projectServiceCmd.AddCommand(serviceDriftCmd)
	serviceDriftCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
}
