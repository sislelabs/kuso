// Dynamic shell completion for the `<project>` and `<service>` positional
// args that nearly every command takes.
//
// Without these, `kuso logs <TAB>` offered nothing — the user had to run
// `kuso get projects` first, read the names, and type one back. Every
// comparable CLI (fly, heroku, railway) completes app names, and the API
// client needed to do it was already wired.
//
// Completion functions must never block a shell for long or write to
// stdout: on any error we return NoFileComp so the shell falls back to
// "no suggestions" rather than dumping a stack trace into the prompt.
package kusoCli

import (
	"encoding/json"

	"github.com/spf13/cobra"
)

// completeProjects suggests project names for the first positional arg.
func completeProjects(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 || api == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	resp, err := api.GetProjects()
	if err != nil || resp.StatusCode() >= 300 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return namesFromList(resp.Body()), cobra.ShellCompDirectiveNoFileComp
}

// completeProjectThenService suggests projects for arg 0 and that
// project's services for arg 1 — the `<project> <service>` shape used by
// logs, env, build, run, shell, db, and most of the rest.
func completeProjectThenService(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if api == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	switch len(args) {
	case 0:
		resp, err := api.GetProjects()
		if err != nil || resp.StatusCode() >= 300 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return namesFromList(resp.Body()), cobra.ShellCompDirectiveNoFileComp
	case 1:
		resp, err := api.GetServices(args[0])
		if err != nil || resp.StatusCode() >= 300 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return shortServiceNames(resp.Body(), args[0]), cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// namesFromList pulls metadata.name out of a CR list payload. Tolerates
// both a bare array and a {"items": [...]} envelope since the API uses
// both shapes depending on endpoint.
func namesFromList(body []byte) []string {
	type item struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Name string `json:"name"`
	}
	var arr []item
	if err := json.Unmarshal(body, &arr); err != nil {
		var env struct {
			Items []item `json:"items"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			return nil
		}
		arr = env.Items
	}
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		if n := it.Metadata.Name; n != "" {
			out = append(out, n)
			continue
		}
		if it.Name != "" {
			out = append(out, it.Name)
		}
	}
	return out
}

// shortServiceNames strips the "<project>-" prefix service CRs carry, so
// completion offers `web` (what every command wants) rather than
// `my-app-web` (the FQ CR name, which most commands reject).
func shortServiceNames(body []byte, project string) []string {
	names := namesFromList(body)
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, shortName(n, project))
	}
	return out
}

// shortName trims a leading "<project>-" from a CR name.
func shortName(name, project string) string {
	p := project + "-"
	if len(name) > len(p) && name[:len(p)] == p {
		return name[len(p):]
	}
	return name
}

// registerCompletions attaches the arg completers to the commands whose
// first two positionals are <project> [service]. Done centrally (rather
// than in each command's own file) so the list is auditable in one place
// and adding a command doesn't silently miss out.
func registerCompletions(root *cobra.Command) {
	// <project> only.
	projectOnly := map[string]bool{
		"status": true, "apply": true,
	}
	// <project> <service>.
	projectService := map[string]bool{
		"logs": true, "redeploy": true, "run": true, "shell": true,
		"env": true, "secret": true, "domains": true, "build": true,
		"db": true, "cron": true, "environment": true, "service": true,
		"revision": true,
	}

	for _, c := range root.Commands() {
		switch {
		case projectOnly[c.Name()]:
			if c.ValidArgsFunction == nil {
				c.ValidArgsFunction = completeProjects
			}
		case projectService[c.Name()]:
			// Parent groups (build, env, db …) dispatch to subcommands,
			// so attach to the leaves; a runnable parent gets it too.
			if c.Runnable() && c.ValidArgsFunction == nil {
				c.ValidArgsFunction = completeProjectThenService
			}
			for _, sub := range c.Commands() {
				if sub.ValidArgsFunction == nil {
					sub.ValidArgsFunction = completeProjectThenService
				}
			}
		}
	}
}
