// kuso user access <username|id> — what a user can see and do, and why.
// Same data as the access modal on the web Users page.

package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

var userAccessOutput string

// resolveUserRef maps a username to its id via the user list; anything
// that matches no username is passed through as an id.
func resolveUserRef(ref string) (string, error) {
	resp, err := api.ListUsers()
	if err := checkRespErr(resp, err); err != nil {
		return "", fmt.Errorf("list users: %w", err)
	}
	var users []struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(resp.Body(), &users); err != nil {
		return "", fmt.Errorf("decode users: %w", err)
	}
	for _, u := range users {
		if u.Username == ref {
			return u.ID, nil
		}
	}
	return ref, nil
}

func accessVia(s kusoApi.UserAccessSource) string {
	if s.Via == "group" {
		return "group " + s.GroupName
	}
	return "direct"
}

var userAccessCmd = &cobra.Command{
	Use:   "access <username|id>",
	Short: "Show what a user can access and where each grant comes from (admin)",
	Example: `  kuso user access ivkirov
  kuso user access ivkirov -o json | jq '.projects[].project'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		id, err := resolveUserRef(args[0])
		if err != nil {
			return err
		}
		resp, err := api.GetUserAccess(id)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("user access: %w", err)
		}
		var a kusoApi.UserAccess
		if err := json.Unmarshal(resp.Body(), &a); err != nil {
			return fmt.Errorf("decode access: %w", err)
		}
		switch userAccessOutput {
		case "json":
			return jsonOut(a)
		case "table", "":
		default:
			return fmt.Errorf("unsupported output format %q", userAccessOutput)
		}

		var roleSrc []string
		for _, s := range a.InstanceRoleSources {
			roleSrc = append(roleSrc, s.Role+" via "+accessVia(s))
		}
		var groups []string
		for _, g := range a.Groups {
			groups = append(groups, g.Name)
		}
		fmt.Printf("user:          %s (%s)\n", a.Username, a.UserID)
		fmt.Printf("instance role: %s", dashIfEmpty(a.InstanceRole))
		if len(roleSrc) > 0 {
			fmt.Printf("  [%s]", strings.Join(roleSrc, ", "))
		}
		fmt.Println()
		fmt.Printf("groups:        %s\n", dashIfEmpty(strings.Join(groups, ", ")))
		if a.Pending {
			fmt.Println("status:        pending (no usable access yet)")
		}
		if a.AllProjects {
			fmt.Println("projects:      all (instance admin)")
		}
		if len(a.Projects) == 0 {
			if !a.AllProjects {
				fmt.Println("projects:      none")
			}
			return nil
		}
		t := tablewriter.NewWriter(os.Stdout)
		t.SetHeader([]string{"PROJECT", "ROLE", "FROM"})
		for _, p := range a.Projects {
			var from []string
			for _, s := range p.Sources {
				how := s.Role + " inherited"
				if s.RoleOverride != "" {
					how = s.RoleOverride + " set on grant"
				}
				from = append(from, how+" via "+accessVia(s))
			}
			t.Append([]string{p.Project, p.Role, strings.Join(from, "; ")})
		}
		t.Render()
		return nil
	},
}

func init() {
	userCmd.AddCommand(userAccessCmd)
	userAccessCmd.Flags().StringVarP(&userAccessOutput, "output", "o", "table", "output format [table, json]")
}
