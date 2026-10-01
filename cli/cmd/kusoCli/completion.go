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
	"strings"

	"github.com/spf13/cobra"
)

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

// registerCompletions attaches a completer to every runnable command
// that doesn't have its own, driven by the placeholders in its Use line
// (<project>, <service>, <addon>, [file] …). It used to be a hand-written
// map by top-level name, which suggested services for `db sql`'s addon
// argument, project names for `apply [file]`, and nothing at all for
// most of the tree.
func registerCompletions(root *cobra.Command) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c != root && c.Runnable() && c.ValidArgsFunction == nil && len(positionalSlots(c.Use)) > 0 {
			c.ValidArgsFunction = completeFromUse
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

// positionalSlots returns the lower-cased positional placeholder names
// from a Use line, stopping at the first flag-looking token.
func positionalSlots(use string) []string {
	fields := strings.Fields(use)
	if len(fields) < 2 {
		return nil
	}
	var out []string
	for _, tok := range fields[1:] {
		if strings.HasPrefix(tok, "--") || strings.HasPrefix(tok, "[--") {
			break
		}
		out = append(out, strings.ToLower(strings.Trim(tok, "<>[].…")))
	}
	return out
}

func isFileSlot(slot string) bool {
	return slot == "file" || slot == "path" || strings.HasSuffix(slot, ".yml") || strings.HasSuffix(slot, ".yaml")
}

func completeFromUse(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	slots := positionalSlots(cmd.Use)
	if len(args) >= len(slots) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	slot := slots[len(args)]
	if isFileSlot(slot) {
		return nil, cobra.ShellCompDirectiveDefault
	}
	if api == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	underProject := len(args) > 0 && slots[0] == "project"
	switch {
	case slot == "project", slot == "name" && len(args) == 0 && cmd.Parent() != nil && cmd.Parent().Name() == "project":
		resp, err := api.GetProjects()
		if err != nil || resp.StatusCode() >= 300 {
			break
		}
		return namesFromList(resp.Body()), cobra.ShellCompDirectiveNoFileComp
	case slot == "service" && underProject:
		resp, err := api.GetServices(args[0])
		if err != nil || resp.StatusCode() >= 300 {
			break
		}
		return shortServiceNames(resp.Body(), args[0]), cobra.ShellCompDirectiveNoFileComp
	case slot == "addon" && underProject:
		resp, err := api.GetAddonsForProject(args[0])
		if err != nil || resp.StatusCode() >= 300 {
			break
		}
		return shortServiceNames(resp.Body(), args[0]), cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}
