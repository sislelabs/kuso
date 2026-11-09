package kusoCli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// revisionRow is one revision as GET .../revisions/{kind}/{name} returns it.
type revisionRow struct {
	ID          string          `json:"id"`
	Project     string          `json:"project"`
	Kind        string          `json:"kind"`
	Name        string          `json:"name"`
	Summary     string          `json:"summary"`
	Actor       string          `json:"actor"`
	CreatedAt   string          `json:"createdAt"`
	Snapshot    json.RawMessage `json:"snapshot"`
	ChangedKeys []string        `json:"changedKeys,omitempty"`
}

type revisionSnapshot struct {
	Patch         map[string]json.RawMessage `json:"patch"`
	Op            string                     `json:"op"`
	Informational bool                       `json:"informational"`
	RevertOf      string                     `json:"revertOf"`
}

func (r revisionRow) snapshot() revisionSnapshot {
	var s revisionSnapshot
	_ = json.Unmarshal(r.Snapshot, &s)
	return s
}

// revisionChangedKeys is what a revision changed: the top-level keys of a
// PATCH snapshot, or "op:<op>" for a whole-list snapshot. nil = not
// replayable. Mirrors changedKeys in web/src/features/revisions/previous.ts.
func revisionChangedKeys(r revisionRow) []string {
	s := r.snapshot()
	if s.Informational {
		return nil
	}
	if len(s.Patch) > 0 {
		keys := make([]string, 0, len(s.Patch))
		for k := range s.Patch {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}
	if s.Op != "" {
		return []string{"op:" + s.Op}
	}
	return nil
}

// revisionChangedText is the CHANGED cell: the fields a patch touched or
// the env keys a secret write named, plus the revision it rolled back.
func revisionChangedText(r revisionRow) string {
	var out string
	switch {
	case len(r.ChangedKeys) > 0:
		out = "env " + strings.Join(r.ChangedKeys, ", ")
	default:
		if keys := revisionChangedKeys(r); len(keys) > 0 {
			out = strings.Join(keys, ", ")
		}
	}
	if of := r.snapshot().RevertOf; of != "" {
		if len(of) > 8 {
			of = of[:8]
		}
		out = strings.TrimSpace(out + " (revert of " + of + ")")
	}
	return truncate(out, 40)
}

func sortRevisionsNewestFirst(revs []revisionRow) []revisionRow {
	out := make([]revisionRow, 0, len(revs))
	for _, r := range revs {
		if _, err := time.Parse(time.RFC3339, r.CreatedAt); err == nil {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339, out[i].CreatedAt)
		b, _ := time.Parse(time.RFC3339, out[j].CreatedAt)
		return a.After(b)
	})
	return out
}

// Fields a service starts without, and how a PATCH puts each back.
// Mirrors DEFAULTS in previous.ts.
var revisionDefaults = map[string]struct {
	apply func(*kusoApi.PatchServiceRequest)
	label string
}{
	"healthcheck": {func(r *kusoApi.PatchServiceRequest) { r.Healthcheck = &kusoApi.Healthcheck{} }, "the default TCP port check"},
	"command":     {func(r *kusoApi.PatchServiceRequest) { r.Command = &[]string{} }, "the image's own start command"},
	"release":     {func(r *kusoApi.PatchServiceRequest) { r.Release = &kusoApi.PatchReleaseRequest{Clear: true} }, "no release command"},
}

// undoPlan is what `revision undo` does about the newest config change:
// replay the newest older revision that set exactly the same fields
// (target), or, when there is none, PATCH those fields back to their
// defaults. Mirrors useRolloutRevert in the web.
type undoPlan struct {
	change       revisionRow
	target       *revisionRow
	defaultPatch *kusoApi.PatchServiceRequest
	defaultLabel string
}

func planUndo(revs []revisionRow) (*undoPlan, error) {
	sorted := sortRevisionsNewestFirst(revs)
	idx := -1
	for i, r := range sorted {
		if revisionChangedKeys(r) != nil {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("no config change to undo")
	}
	change := sorted[idx]
	keys := revisionChangedKeys(change)
	same := func(a, b []string) bool {
		return strings.Join(a, "\x00") == strings.Join(b, "\x00")
	}
	for _, r := range sorted[idx+1:] {
		if k := revisionChangedKeys(r); k != nil && same(k, keys) {
			r := r
			return &undoPlan{change: change, target: &r}, nil
		}
	}
	patch := &kusoApi.PatchServiceRequest{}
	var labels []string
	for _, k := range keys {
		d, ok := revisionDefaults[k]
		if !ok {
			return nil, fmt.Errorf("the newest change (%s) has no earlier value and no default to go back to; pick a revision with 'kuso revision revert'", strings.Join(keys, ", "))
		}
		d.apply(patch)
		labels = append(labels, d.label)
	}
	return &undoPlan{change: change, defaultPatch: patch, defaultLabel: strings.Join(labels, " and ")}, nil
}

// describe reads after "undo" / "undid": "the healthcheck change from 2m
// ago by re-applying revision <id> (17h ago)".
func (p *undoPlan) describe() string {
	what := fmt.Sprintf("the %s change from %s ago", strings.Join(revisionChangedKeys(p.change), ", "), relativeAge(p.change.CreatedAt))
	if p.target != nil {
		return fmt.Sprintf("%s by re-applying revision %s (%s ago)", what, p.target.ID, relativeAge(p.target.CreatedAt))
	}
	return fmt.Sprintf("%s by going back to %s", what, p.defaultLabel)
}

var (
	revisionUndoYes    bool
	revisionUndoDryRun bool
)

var revisionUndoCmd = &cobra.Command{
	Use:   "undo <project> <service>",
	Short: "Undo a service's newest config change (Revert to previous / Revert to default)",
	Long: `Undo the newest replayable config change on a service: the dashboard's
"Revert to previous" / "Revert to default" on a failing rollout.

If an older revision set exactly the same fields, it is replayed. If the
change set something the service had never set before (a first health
check, a run command, a release hook), those fields go back to their
defaults instead. It's a service setting, so every environment rolls.
--dry-run prints the plan without changing anything.`,
	Example: `  kuso revision undo shop api --dry-run
  kuso revision undo shop api --yes`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project, service := args[0], args[1]
		resp, err := api.ListRevisions(project, "service", service)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list revisions: %w", err)
		}
		var revs []revisionRow
		if err := json.Unmarshal(resp.Body(), &revs); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		plan, err := planUndo(revs)
		if err != nil {
			return err
		}
		if revisionUndoDryRun {
			fmt.Printf("would undo %s\n", plan.describe())
			return nil
		}
		if err := confirmDestructive(revisionUndoYes, fmt.Sprintf("%s/%s: undo %s? Every environment rolls.", project, service, plan.describe())); err != nil {
			return err
		}
		if plan.target != nil {
			resp, err = api.RevertRevision(project, plan.target.ID)
		} else {
			resp, err = api.PatchService(project, service, *plan.defaultPatch)
		}
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("undo: %w", err)
		}
		fmt.Printf("%s/%s: undid %s; follow the rollout with 'kuso status %s'\n", project, service, plan.describe(), project)
		return nil
	},
}

func init() {
	revisionCmd.AddCommand(revisionUndoCmd)
	revisionUndoCmd.Flags().BoolVarP(&revisionUndoYes, "yes", "y", false, "skip the confirmation prompt")
	revisionUndoCmd.Flags().BoolVar(&revisionUndoDryRun, "dry-run", false, "print what would be undone and change nothing")
}
