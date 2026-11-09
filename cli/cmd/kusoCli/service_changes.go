// service_changes.go — batched service edits: variable writes, removals
// and a spec patch applied in one request so each env rolls once, not
// once per change (POST .../services/{s}/changes, the dashboard's
// "Apply changes"). Used by `kuso service apply-changes` and by
// `kuso env set` / `kuso env unset` when they are given several keys.

package kusoCli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// errChangesUnsupported: the server predates POST .../changes.
var errChangesUnsupported = fmt.Errorf("this server has no batched-changes endpoint")

// applyServiceChanges sends one batch. Per-item failures and a failed
// rollout write come back as an error naming each failure, after the
// successful items have been applied.
func applyServiceChanges(project, service string, req kusoApi.ServiceChangesRequest) (*kusoApi.ServiceChangesResult, error) {
	resp, err := api.ApplyServiceChanges(project, service, req)
	if err != nil {
		return nil, err
	}
	// An older server answers its catch-all "no API route" 404; a missing
	// service is a 404 too, but with the service's name in the message.
	if (resp.StatusCode() == 404 && strings.Contains(string(resp.Body()), "no API route")) || resp.StatusCode() == 405 {
		return nil, errChangesUnsupported
	}
	if err := checkRespErr(resp, nil); err != nil {
		return nil, err
	}
	var res kusoApi.ServiceChangesResult
	if err := json.Unmarshal(resp.Body(), &res); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &res, changesFailure(&res)
}

func changesFailure(res *kusoApi.ServiceChangesResult) error {
	var failed []string
	for _, r := range res.Results {
		if r.OK {
			continue
		}
		what := r.Kind
		if r.Name != "" {
			what += " " + r.Name
		}
		failed = append(failed, what+": "+r.Error)
	}
	if res.FlushError != "" {
		failed = append(failed, "rollout: "+res.FlushError)
	}
	if len(failed) == 0 {
		return nil
	}
	return fmt.Errorf("%d of the changes failed (the others are applied):\n  %s", len(failed), strings.Join(failed, "\n  "))
}

func rolloutNote(res *kusoApi.ServiceChangesResult) string {
	if res.RolledOut {
		return "one rollout"
	}
	return "nothing to roll out"
}

func parseKeyValues(kvs []string) ([]kusoApi.ServiceChangeEnvSet, error) {
	out := make([]kusoApi.ServiceChangeEnvSet, 0, len(kvs))
	for _, kv := range kvs {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("argument %q is not KEY=VALUE", kv)
		}
		out = append(out, kusoApi.ServiceChangeEnvSet{Name: kv[:eq], Value: kv[eq+1:]})
	}
	return out, nil
}

func newServiceApplyChangesCmd() *cobra.Command {
	var sets, unsets []string
	var patchFile, message string
	var yes bool
	c := &cobra.Command{
		Use:   "apply-changes <project> <service>",
		Short: "Apply several variable and spec edits with a single rollout",
		Long: `Apply variable writes (--set), removals (--unset) and one spec patch
(--patch-file, a PATCH body as JSON, e.g. {"port":8080}) together, so each
environment of the service rolls once instead of once per change.

Items are applied independently: a failed item is reported and left out,
the rest are applied and rolled out. Values are stored the way
'kuso env set' stores them (the server picks secret or plain).`,
		Example: `  kuso service apply-changes shop api --set LOG_LEVEL=debug --set FEATURE_X=1 --unset OLD_FLAG
  kuso service apply-changes shop api --set PORT=8080 --patch-file port.json -m "move to 8080"
  echo '{"healthcheck":{"path":"/healthz"}}' | kuso service apply-changes shop api --patch-file -`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			req := kusoApi.ServiceChangesRequest{EnvUnset: unsets, Message: message}
			var err error
			if req.EnvSet, err = parseKeyValues(sets); err != nil {
				return err
			}
			if patchFile != "" {
				var raw []byte
				if patchFile == "-" {
					raw, err = io.ReadAll(os.Stdin)
				} else {
					raw, err = os.ReadFile(patchFile)
				}
				if err != nil {
					return fmt.Errorf("read --patch-file: %w", err)
				}
				var obj map[string]any
				if err := json.Unmarshal(raw, &obj); err != nil {
					return fmt.Errorf("--patch-file must be a JSON object: %w", err)
				}
				req.Patch = raw
			}
			if len(req.EnvSet) == 0 && len(req.EnvUnset) == 0 && req.Patch == nil {
				return fmt.Errorf("nothing to apply: pass --set, --unset or --patch-file")
			}
			if len(req.EnvUnset) > 0 {
				if err := confirmDestructive(yes, fmt.Sprintf("Remove %s from %s/%s? The values are not recoverable.",
					strings.Join(req.EnvUnset, ", "), args[0], args[1])); err != nil {
					return err
				}
			}
			res, err := applyServiceChanges(args[0], args[1], req)
			if err == errChangesUnsupported {
				return fmt.Errorf("%w; upgrade it, or apply the changes one by one with kuso env set / kuso service set", err)
			}
			if res != nil {
				fmt.Printf("applied changes to %s/%s (%s)\n", args[0], args[1], rolloutNote(res))
			}
			return err
		},
	}
	c.Flags().StringArrayVar(&sets, "set", nil, "KEY=VALUE to write, repeatable")
	c.Flags().StringArrayVar(&unsets, "unset", nil, "KEY to remove, repeatable")
	c.Flags().StringVar(&patchFile, "patch-file", "", "JSON service patch to apply in the same rollout ('-' reads stdin)")
	c.Flags().StringVarP(&message, "message", "m", "", "note for the audit log")
	c.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt for --unset")
	return c
}

func init() {
	projectServiceCmd.AddCommand(newServiceApplyChangesCmd())
	serviceCmd.AddCommand(newServiceApplyChangesCmd())
}
