package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var statusOutput string

func init() {
	rootCmd.AddCommand(statusCmd)
	statusCmd.Flags().StringVarP(&statusOutput, "output", "o", "table", "output format: table | json")
}

// statusCmd prints a one-screen view of the project + every service +
// its production env. Pulls from the rollup endpoint /api/projects/{p}.
//
// If the working directory has a kuso.yml we use its `project:` value
// by default; otherwise the user passes the name explicitly.
var statusCmd = &cobra.Command{
	Use:     "status [project]",
	Args:    cobra.MaximumNArgs(1),
	Short:   "Show the project rollup: services, URLs, replicas, last build, addons.",
	Example: "  kuso status\n  kuso status my-product",
	Run: func(cmd *cobra.Command, args []string) {
		project := ""
		if len(args) > 0 {
			project = args[0]
		} else if path, note, err := resolveManifestPath("."); err == nil {
			// Accept BOTH manifest names via the shared resolver so
			// zero-arg status agrees with apply/init.
			if note != "" {
				fmt.Fprintln(os.Stderr, "note:", note)
			}
			if body, rerr := os.ReadFile(path); rerr == nil {
				project = readProjectFromYAML(body)
			}
		}
		if project == "" {
			fmt.Fprintln(os.Stderr, "error: pass <project> or run from a directory containing kuso.yml (or kuso.yaml)")
			os.Exit(1)
		}
		resp, err := api.GetProjectFull(project)
		if err != nil {
			fmt.Fprintln(os.Stderr, "status:", err)
			os.Exit(1)
		}
		if resp.StatusCode() == 404 {
			fmt.Fprintf(os.Stderr, "project %q not found\n", project)
			os.Exit(1)
		}
		if resp.StatusCode() >= 400 {
			fmt.Fprintf(os.Stderr, "status failed: %s\n", apiErrorMessage(resp.StatusCode(), string(resp.Body())))
			os.Exit(1)
		}

		var rollup struct {
			Project struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Spec struct {
					BaseDomain    string                      `json:"baseDomain"`
					NetworkPolicy *statusProjectNetworkPolicy `json:"networkPolicy,omitempty"`
				} `json:"spec"`
			} `json:"project"`
			Services []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Spec struct {
					Runtime string `json:"runtime"`
					Port    int    `json:"port"`
				} `json:"spec"`
			} `json:"services"`
			Environments []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Spec struct {
					Service string `json:"service"`
					Kind    string `json:"kind"`
					Host    string `json:"host"`
					Branch  string `json:"branch"`
				} `json:"spec"`
				Status map[string]any `json:"status"`
			} `json:"environments"`
		}
		if err := json.Unmarshal(resp.Body(), &rollup); err != nil {
			fmt.Fprintln(os.Stderr, "decode:", err)
			os.Exit(1)
		}

		if statusOutput == "json" {
			// Re-emit the parsed shape so scripts get a stable schema
			// even if the server adds fields later, plus the per-service
			// last build and the addons the table shows (they were
			// missing, so scripts couldn't get the advertised rollup).
			type svcOut struct {
				Metadata  any          `json:"metadata"`
				Spec      any          `json:"spec"`
				LastBuild *statusBuild `json:"lastBuild"`
			}
			doc := struct {
				Project      any              `json:"project"`
				Services     []svcOut         `json:"services"`
				Environments any              `json:"environments"`
				Addons       []map[string]any `json:"addons"`
			}{Project: rollup.Project, Environments: rollup.Environments, Services: []svcOut{}, Addons: []map[string]any{}}
			for _, s := range rollup.Services {
				so := svcOut{Metadata: s.Metadata, Spec: s.Spec}
				if br, err := api.ListBuilds(project, short(s.Metadata.Name, rollup.Project.Metadata.Name)); err == nil && br.StatusCode() < 300 {
					var builds []statusBuild
					if json.Unmarshal(br.Body(), &builds) == nil {
						so.LastBuild = latestBuild(builds)
					}
				}
				doc.Services = append(doc.Services, so)
			}
			if ar, err := api.GetAddonsForProject(project); err == nil && ar.StatusCode() < 300 {
				_ = json.Unmarshal(ar.Body(), &doc.Addons)
			}
			out, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				fmt.Fprintln(os.Stderr, "encode:", err)
				os.Exit(1)
			}
			fmt.Println(string(out))
			return
		}

		fmt.Printf("project %s\n", rollup.Project.Metadata.Name)
		if rollup.Project.Spec.BaseDomain != "" {
			fmt.Printf("  base   %s\n", rollup.Project.Spec.BaseDomain)
		}
		if w := isolationWarning(rollup.Project.Metadata.Name, rollup.Project.Spec.NetworkPolicy); w != "" {
			fmt.Printf("  warning: %s\n", w)
		}
		for _, s := range rollup.Services {
			fmt.Printf("\nservice %s   runtime=%s port=%d\n",
				short(s.Metadata.Name, rollup.Project.Metadata.Name), s.Spec.Runtime, s.Spec.Port)
			for _, e := range rollup.Environments {
				if e.Spec.Service != s.Metadata.Name {
					continue
				}
				phase, _ := e.Status["phase"].(string)
				if phase == "" {
					phase = "unknown"
				}
				url, _ := e.Status["url"].(string)
				replicaInfo := "-"
				if r, ok := e.Status["replicas"].(map[string]any); ok {
					ready, _ := r["ready"].(float64)
					desired, _ := r["desired"].(float64)
					replicaInfo = fmt.Sprintf("%d/%d", int(ready), int(desired))
				}
				// Unified rollup (server-derived status.state) — the single
				// "is my app up?" answer. Appended (not replacing phase) so
				// existing output-parsing scripts keep their columns; absent
				// on pre-rollup servers, in which case the line is unchanged.
				state, _ := e.Status["state"].(string)
				stateCol := ""
				if state != "" {
					stateCol = fmt.Sprintf("  state=%s (%s)", state, serviceStatusLabel(state))
				}
				fmt.Printf("  %s  %-10s replicas=%s%s\n", e.Spec.Kind, phase, replicaInfo, stateCol)
				// The plain-language reason for any state that isn't Live,
				// plus the failing-rollout / stuck facts behind it. Quiet
				// when healthy to keep the screen calm.
				if reason := envStatusReason(e.Status); reason != "" {
					fmt.Printf("    %s\n", reason)
				}
				for _, line := range envStatusFacts(e.Status, time.Now()) {
					fmt.Printf("    %s\n", line)
				}
				if url != "" {
					fmt.Printf("    %s\n", url)
				}
				// Print the branch for EVERY env, not just production.
				// Suppressing it elsewhere hid the most diagnostic field
				// on staging/preview envs — and this is precisely where
				// it matters, since a project default-branch change that
				// fails to restamp env.spec.branch leaves an env
				// silently promoting nothing. `kuso get envs` always
				// showed it; status disagreeing made the two views
				// contradict each other.
				if e.Spec.Branch != "" {
					fmt.Printf("    branch %s\n", e.Spec.Branch)
				}
			}
			svcShort := short(s.Metadata.Name, rollup.Project.Metadata.Name)
			if br, err := api.ListBuilds(project, svcShort); err == nil && br.StatusCode() < 300 {
				var builds []statusBuild
				if json.Unmarshal(br.Body(), &builds) == nil {
					if line := lastBuildLine(builds); line != "" {
						fmt.Printf("  %s\n", line)
					}
				}
			}
		}
		if er, err := api.RawGet("/api/projects/" + escPath(project) + "/errors/summary"); err == nil && er.StatusCode() < 300 {
			fmt.Print(errorSummaryLine(project, er.Body()))
		}
		if ar, err := api.GetAddonsForProject(project); err == nil && ar.StatusCode() < 300 {
			var addons []map[string]any
			if json.Unmarshal(ar.Body(), &addons) == nil {
				fmt.Print(addonsStatusLines(project, addons))
			}
		}
	},
}

type statusBuild struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	Branch       string `json:"branch"`
	StartedAt    string `json:"startedAt"`
	CreatedAt    string `json:"createdAt,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// lastBuildLine summarises the newest build. The list is live CRs
// followed by archived records, so its order isn't trusted.
func lastBuildLine(builds []statusBuild) string {
	latest := latestBuild(builds)
	if latest == nil {
		return "last build: none"
	}
	line := fmt.Sprintf("last build: %s  %s", latest.Status, latest.ID)
	if latest.Branch != "" {
		line += "  branch=" + latest.Branch
	}
	if latest.Status == "failed" && latest.ErrorMessage != "" {
		line += "\n    " + latest.ErrorMessage
	}
	return line
}

// latestBuild ranks by creation time, falling back to startedAt for
// archived records (the server sends createdAt for live builds only).
// Ranking by startedAt alone hid a queued build, or one that failed
// before it started, behind the previous green build.
func latestBuild(builds []statusBuild) *statusBuild {
	var latest *statusBuild
	var latestAt time.Time
	for i := range builds {
		at := builds[i].CreatedAt
		if at == "" {
			at = builds[i].StartedAt
		}
		t, _ := time.Parse(time.RFC3339, at)
		if latest == nil || t.After(latestAt) {
			latest, latestAt = &builds[i], t
		}
	}
	return latest
}

// addonsStatusLines renders the project's addons as "name (kind version)".
func addonsStatusLines(project string, addons []map[string]any) string {
	if len(addons) == 0 {
		return "\naddons: none\n"
	}
	parts := make([]string, 0, len(addons))
	for _, a := range addons {
		spec := mapAt(a, "spec")
		desc := asString(spec["kind"])
		if v := asString(spec["version"]); v != "" {
			desc += " " + v
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", stripPrefix(resourceName(a), project+"-"), desc))
	}
	sort.Strings(parts)
	return "\naddons: " + strings.Join(parts, ", ") + "\n"
}

func short(full, project string) string {
	prefix := project + "-"
	if len(full) > len(prefix) && full[:len(prefix)] == prefix {
		return full[len(prefix):]
	}
	return full
}

// errorSummaryLine renders GET /errors/summary (open error groups per
// service in the last 24h; resolved groups that haven't recurred don't
// count) as one line. "" when the body can't be read.
func errorSummaryLine(project string, body []byte) string {
	var sum struct {
		Services map[string]struct {
			Occurrences int64 `json:"occurrences"`
			Groups      int64 `json:"groups"`
		} `json:"services"`
	}
	if json.Unmarshal(body, &sum) != nil {
		return ""
	}
	if len(sum.Services) == 0 {
		return "\nerrors (24h): none\n"
	}
	names := make([]string, 0, len(sum.Services))
	for n := range sum.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		s := sum.Services[n]
		parts = append(parts, fmt.Sprintf("%s %d in %d group(s)", n, s.Occurrences, s.Groups))
	}
	return "\nerrors (24h): " + strings.Join(parts, ", ") + "  (kuso service errors " + project + " <service>)\n"
}

type statusProjectNetworkPolicy struct {
	Enabled            *bool `json:"enabled"`
	EnvIsolation       *bool `json:"envIsolation"`
	IsolateNonProdEnvs *bool `json:"isolateNonProdEnvs"`
}

// isolationWarning is the status line for a project whose envs aren't
// network-isolated (ISO-5): projects created before isolation stay flat
// until someone turns it on.
func isolationWarning(project string, np *statusProjectNetworkPolicy) string {
	on := func(b *bool) bool { return b != nil && *b }
	switch {
	case np != nil && np.Enabled != nil && !*np.Enabled:
		return ""
	case np == nil || !on(np.EnvIsolation):
		return "env isolation off: previews and staging can reach production pods and addons (kuso project isolation enable " + project + ")"
	case !on(np.IsolateNonProdEnvs):
		return "non-production envs can reach each other (kuso project isolation enable " + project + ")"
	}
	return ""
}
