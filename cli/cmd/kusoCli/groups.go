// Command grouping for `kuso --help`.
//
// The CLI has ~50 top-level commands. Ungrouped, cobra prints them as one
// flat alphabetical wall where `alert` sits next to `apply` sits next to
// `audit` — nothing tells a newcomer (human or agent) which three commands
// they actually need to ship an app, or which ones are cluster-operator
// surface they should leave alone.
//
// Groups are assigned here rather than in each command's own file so the
// whole taxonomy is visible in one place; adding a command without a group
// is not an error — it lands under "Additional Commands".
package kusoCli

import "github.com/spf13/cobra"

// Group IDs. Ordered by how often you reach for them: the deploy loop
// first, then the things you configure once, then operator surface.
const (
	groupDeploy   = "deploy"
	groupInspect  = "inspect"
	groupData     = "data"
	groupConfig   = "config"
	groupAccess   = "access"
	groupPlatform = "platform"
	groupSetup    = "setup"
)

// registerCommandGroups declares the groups and assigns every top-level
// command to one. Commands are looked up by name so this stays decoupled
// from the init()-registration order in each file.
func registerCommandGroups(root *cobra.Command) {
	root.AddGroup(
		&cobra.Group{ID: groupDeploy, Title: "Ship code:"},
		&cobra.Group{ID: groupInspect, Title: "Inspect & debug:"},
		&cobra.Group{ID: groupData, Title: "Data & addons:"},
		&cobra.Group{ID: groupConfig, Title: "Configure a project:"},
		&cobra.Group{ID: groupAccess, Title: "Users & access:"},
		&cobra.Group{ID: groupPlatform, Title: "Cluster & platform:"},
		&cobra.Group{ID: groupSetup, Title: "Setup & session:"},
	)

	assign := map[string]string{
		// The deploy loop — what you use every day.
		"build":       groupDeploy,
		"redeploy":    groupDeploy,
		"run":         groupDeploy,
		"apply":       groupDeploy,
		"service":     groupDeploy,
		"environment": groupDeploy,
		"cron":        groupDeploy,
		"revision":    groupDeploy,

		// Finding out what's going on.
		"status":        groupInspect,
		"logs":          groupInspect,
		"get":           groupInspect,
		"shell":         groupInspect,
		"doctor":        groupInspect,
		"health":        groupInspect,
		"incident":      groupInspect,
		"alert":         groupInspect,
		"audit":         groupInspect,
		"notifications": groupInspect,
		"usage":         groupInspect,

		// Databases, caches, queues, and their backups.
		"db":             groupData,
		"backup":         groupData,
		"restore":        groupData,
		"addon-backup":   groupData,
		"marketplace":    groupData,
		"instance-addon": groupData,
		"instance-pg":    groupData,

		// Per-project configuration.
		"project":       groupConfig,
		"env":           groupConfig,
		"env-group":     groupConfig,
		"secret":        groupConfig,
		"shared-secret": groupConfig,
		"domains":       groupConfig,

		// Who can do what.
		"user":   groupAccess,
		"group":  groupAccess,
		"role":   groupAccess,
		"invite": groupAccess,
		"token":  groupAccess,

		// Cluster-operator surface. NOTE: there is no top-level
		// "instance" or "grant" command — instances are managed via
		// `remote`, and grants nest under `project grant`. Entries for
		// them here would be silent no-ops (the loop below matches on
		// cmd.Name()), so don't add them back.
		"node":            groupPlatform,
		"instance-config": groupPlatform,
		"instance-secret": groupPlatform,
		"incident-agent":  groupPlatform,
		"upgrade":         groupPlatform,
		"ssh-key":         groupPlatform,
		"remote":          groupPlatform,

		// First-run and session.
		"login":   groupSetup,
		"init":    groupSetup,
		"github":  groupSetup,
		"import":  groupSetup,
		"migrate": groupSetup,
		"api":     groupSetup,
		"version": groupSetup,
	}

	for _, c := range root.Commands() {
		if id, ok := assign[c.Name()]; ok {
			c.GroupID = id
		}
	}
}
