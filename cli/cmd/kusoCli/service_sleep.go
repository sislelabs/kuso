// service_sleep.go: `kuso project service sleep` (and the top-level
// `kuso service sleep` alias) — view or toggle scale-to-zero.
//
//	kuso project service sleep <project> <service>                 (show)
//	kuso project service sleep <project> <service> on|off [--after 30m] [--non-production on|off]

package kusoCli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

var (
	serviceSleepAfter   string
	serviceSleepNonProd string
)

var serviceSleepCmd = &cobra.Command{
	Use:   "sleep <project> <service> [on|off]",
	Short: "Show or toggle scale-to-zero (sleep when idle) for a service",
	Long: `Show or change when a service's envs scale to zero after being idle.
The first request after that wakes the env back up (a cold start of a
few seconds).

  on|off            production env: sleep when idle (off by default)
  --non-production  every other env (named envs like staging, env-group
                    clones, PR previews): sleeps by default; "off" keeps
                    them always on
  --after           idle window before sleeping (default 30m), shared by
                    production and non-production envs

With no state and no flags, prints the current settings.

Don't enable production sleep for services that receive third-party
webhooks or payment callbacks: the cold start can outlast the sender's
timeout.`,
	Example: `  kuso project service sleep scubatony api
  kuso project service sleep scubatony api on --after 15m
  kuso project service sleep scubatony api off
  kuso project service sleep scubatony api --non-production off`,
	Args: cobra.RangeArgs(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project, service := args[0], args[1]
		state := ""
		if len(args) == 3 {
			state = args[2]
		}
		if state == "" && !cmd.Flags().Changed("after") && !cmd.Flags().Changed("non-production") {
			cur, err := fetchSleep(project, service)
			if err != nil {
				return err
			}
			fmt.Println(describeSleep(cur))
			return nil
		}
		req, err := buildSleepPatch(state, serviceSleepAfter, serviceSleepNonProd)
		if err != nil {
			return err
		}
		resp, err := api.PatchServiceSleep(project, service, kusoApi.PatchSleepBody{Sleep: req})
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("update sleep: %w", err)
		}
		cur, err := fetchSleep(project, service)
		if err != nil {
			return err
		}
		// A server without nonProduction support drops the field silently;
		// say so instead of printing a setting that didn't take.
		if req.NonProduction != "" && cur.nonProduction() != req.NonProduction {
			fmt.Printf("warning: the server did not store --non-production=%s (it needs a kuso-server that supports sleep.nonProduction)\n", req.NonProduction)
		}
		fmt.Printf("service %s/%s: %s\n", project, service, describeSleep(cur))
		return nil
	},
}

// sleepWire is the subset of the service read we need.
type sleepWire struct {
	Enabled       bool   `json:"enabled"`
	AfterMinutes  int    `json:"afterMinutes"`
	NonProduction string `json:"nonProduction"`
	WakeOn        *struct {
		ExcludePaths []string `json:"excludePaths"`
	} `json:"wakeOn"`
}

func (s sleepWire) nonProduction() string {
	if s.NonProduction == "" {
		return "on"
	}
	return s.NonProduction
}

func fetchSleep(project, service string) (sleepWire, error) {
	resp, err := api.GetService(project, service)
	if err := checkRespErr(resp, err); err != nil {
		return sleepWire{}, fmt.Errorf("fetch service: %w", err)
	}
	var w struct {
		Spec struct {
			Sleep *sleepWire `json:"sleep"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(resp.Body(), &w); err != nil {
		return sleepWire{}, fmt.Errorf("decode service: %w", err)
	}
	if w.Spec.Sleep == nil {
		return sleepWire{}, nil
	}
	return *w.Spec.Sleep, nil
}

func describeSleep(s sleepWire) string {
	after := s.AfterMinutes
	if after <= 0 {
		after = 30
	}
	prod := "off"
	if s.Enabled {
		prod = "on"
	}
	out := fmt.Sprintf("sleep production=%s non-production=%s after=%dm", prod, s.nonProduction(), after)
	if s.WakeOn != nil && len(s.WakeOn.ExcludePaths) > 0 {
		out += fmt.Sprintf(" (kept warm by wakeOn.excludePaths %s: no env sleeps)", strings.Join(s.WakeOn.ExcludePaths, ","))
	}
	return out
}

// buildSleepPatch validates the user's inputs into a PATCH body. Empty
// inputs leave that field alone; at least one must be set.
func buildSleepPatch(state, after, nonProd string) (kusoApi.PatchSleepRequest, error) {
	var req kusoApi.PatchSleepRequest
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "":
	case "on":
		v := true
		req.Enabled = &v
	case "off":
		v := false
		req.Enabled = &v
	default:
		return req, fmt.Errorf("state must be on|off (got %q)", state)
	}
	if strings.TrimSpace(after) != "" {
		m, err := parseSleepAfter(after)
		if err != nil {
			return req, err
		}
		req.AfterMinutes = &m
	}
	switch np := strings.ToLower(strings.TrimSpace(nonProd)); np {
	case "":
	case "on", "off":
		req.NonProduction = np
	default:
		return req, fmt.Errorf("--non-production must be on|off (got %q)", nonProd)
	}
	if req.Enabled == nil && req.AfterMinutes == nil && req.NonProduction == "" {
		return req, fmt.Errorf("nothing to change: pass on|off, --after or --non-production")
	}
	return req, nil
}

// parseSleepAfter accepts a Go duration in whole minutes ("30m", "1h30m")
// or a bare minute count ("45").
func parseSleepAfter(s string) (int, error) {
	s = strings.TrimSpace(s)
	if n, err := strconv.Atoi(s); err == nil {
		if n < 1 {
			return 0, fmt.Errorf("--after must be at least 1 minute (got %q)", s)
		}
		return n, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("--after: %q is not a duration like 30m or 1h", s)
	}
	if d < time.Minute || d%time.Minute != 0 {
		return 0, fmt.Errorf("--after must be a whole number of minutes, at least 1m (got %q)", s)
	}
	return int(d / time.Minute), nil
}

func init() {
	serviceSleepCmd.Flags().StringVar(&serviceSleepAfter, "after", "", "idle window before sleeping, e.g. 30m or 1h (default 30m)")
	serviceSleepCmd.Flags().StringVar(&serviceSleepNonProd, "non-production", "", "on|off: whether non-production envs sleep when idle (default on)")
	projectServiceCmd.AddCommand(serviceSleepCmd)
	// Registered here rather than in service_toplevel_alias.go: aliasOf
	// copies flags, so it must run after the flags above exist.
	serviceCmd.AddCommand(aliasOf(serviceSleepCmd))
}
