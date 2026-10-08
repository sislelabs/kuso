package github

import (
	"context"
	"fmt"
	"sort"

	gogithub "github.com/google/go-github/v66/github"
)

// RequiredInstallationPermissions are the permissions kuso needs that an
// App created from an older manifest may lack: commit statuses on every
// build, and checks for the wait-for-CI gate. Editing the App's
// permissions on GitHub doesn't apply them until each installation
// accepts, so the installation is what gets checked.
var RequiredInstallationPermissions = map[string]string{
	"statuses": "write",
	"checks":   "read",
}

// PermissionGap is one installation missing required permissions,
// rendered as "<permission>:<level>" in sorted order.
type PermissionGap struct {
	InstallationID int64    `json:"installationId"`
	Account        string   `json:"account"`
	Missing        []string `json:"missing"`
}

// InstallationPermissionGaps lists every installation whose granted
// permissions fall short of RequiredInstallationPermissions.
func (c *Client) InstallationPermissionGaps(ctx context.Context) ([]PermissionGap, error) {
	cli := c.App()
	out := []PermissionGap{}
	page := 1
	for {
		insts, resp, err := cli.Apps.ListInstallations(ctx, &gogithub.ListOptions{Page: page, PerPage: 100})
		if err != nil {
			return nil, fmt.Errorf("github: list installations: %w", err)
		}
		for _, ins := range insts {
			granted := map[string]string{
				"statuses": ins.GetPermissions().GetStatuses(),
				"checks":   ins.GetPermissions().GetChecks(),
			}
			var missing []string
			for perm, level := range RequiredInstallationPermissions {
				if !permissionCovers(granted[perm], level) {
					missing = append(missing, perm+":"+level)
				}
			}
			if len(missing) > 0 {
				sort.Strings(missing)
				out = append(out, PermissionGap{
					InstallationID: ins.GetID(),
					Account:        ins.GetAccount().GetLogin(),
					Missing:        missing,
				})
			}
		}
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		page = resp.NextPage
	}
}

func permissionCovers(granted, want string) bool {
	switch want {
	case "read":
		return granted == "read" || granted == "write" || granted == "admin"
	case "write":
		return granted == "write" || granted == "admin"
	default:
		return granted == want
	}
}
