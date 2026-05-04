// Inventory: one-pass gather of every project / app / service /
// database from the Coolify instance, joined by environment_id so
// each resource knows which Coolify project it belongs to. Used by
// the migration command to render the report and drive the apply.

package coolify

import (
	"fmt"
	"strings"
)

// Item is the unified shape every Coolify resource decays into for
// reporting + apply. We don't preserve the exact source struct
// because the downstream code only cares about the migration-
// relevant bits.
type Item struct {
	Verdict     Verdict
	UUID        string
	Name        string
	ProjectName string // Coolify project this lives in
	EnvName     string // Coolify environment ("production", etc.)
	// One of App / Service / Database is set depending on the
	// underlying Coolify kind. The classifier already inspected the
	// fields it needed; downstream code reaches into these for the
	// remaining migration logic (repo URL, db URL, etc.).
	App      *Application
	Service  *Service
	Database *Database
}

// Inventory is the collected snapshot. Items are flat — the report
// groups by ProjectName at print time.
type Inventory struct {
	CoolifyVersion string
	Projects       []Project
	Items          []Item
	// Stats: pre-computed counters so the report header doesn't have
	// to walk Items twice.
	NumApps    int
	NumDBs     int
	NumServices int
	NumSkipped int
	NumMigrate int
	NumFlag    int
}

// Snapshot collects every resource from the Coolify instance and
// classifies each. Read-only; never mutates anything. Returns a
// fully-populated Inventory or the first error.
func Snapshot(c *Client) (*Inventory, error) {
	if err := c.AssertReadOnly(); err != nil {
		return nil, err
	}
	inv := &Inventory{}
	if v, err := c.Version(); err == nil {
		inv.CoolifyVersion = strings.TrimSpace(strings.Trim(v, `"`))
	}

	// Pull projects + hydrate each so we have the environment list.
	// Coolify's /projects returns environments=[] in the list view,
	// only the per-project endpoint includes them. We need them to
	// look up environment_id → (project, env) for downstream items.
	projs, err := c.ListProjects()
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	envByID := map[int]struct {
		ProjectName string
		EnvName     string
	}{}
	for i := range projs {
		full, err := c.GetProject(projs[i].UUID)
		if err != nil {
			// Soft-fail per project — we'd rather report N-1 than
			// abort the whole snapshot for one broken UUID.
			continue
		}
		projs[i] = full
		for _, env := range full.Environments {
			envByID[env.ID] = struct {
				ProjectName string
				EnvName     string
			}{ProjectName: full.Name, EnvName: env.Name}
		}
	}
	inv.Projects = projs

	// Apps.
	apps, err := c.ListApplications()
	if err != nil {
		return nil, fmt.Errorf("list applications: %w", err)
	}
	for i := range apps {
		a := &apps[i]
		v := ClassifyApplication(*a)
		ctx := envByID[a.EnvironmentID]
		inv.Items = append(inv.Items, Item{
			Verdict:     v,
			UUID:        a.UUID,
			Name:        a.Name,
			ProjectName: ctx.ProjectName,
			EnvName:     ctx.EnvName,
			App:         a,
		})
		inv.NumApps++
		bumpStat(inv, v)
	}

	// Services (always skipped per policy, but surfaced).
	svcs, err := c.ListServices()
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	for i := range svcs {
		s := &svcs[i]
		v := ClassifyService(*s)
		ctx := envByID[s.EnvironmentID]
		inv.Items = append(inv.Items, Item{
			Verdict:     v,
			UUID:        s.UUID,
			Name:        s.Name,
			ProjectName: ctx.ProjectName,
			EnvName:     ctx.EnvName,
			Service:     s,
		})
		inv.NumServices++
		bumpStat(inv, v)
	}

	// Databases.
	dbs, err := c.ListDatabases()
	if err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
	}
	for i := range dbs {
		d := &dbs[i]
		v := ClassifyDatabase(*d)
		ctx := envByID[d.EnvironmentID]
		inv.Items = append(inv.Items, Item{
			Verdict:     v,
			UUID:        d.UUID,
			Name:        d.Name,
			ProjectName: ctx.ProjectName,
			EnvName:     ctx.EnvName,
			Database:    d,
		})
		inv.NumDBs++
		bumpStat(inv, v)
	}
	return inv, nil
}

func bumpStat(inv *Inventory, v Verdict) {
	switch v.Action {
	case "migrate":
		inv.NumMigrate++
	case "skip":
		inv.NumSkipped++
	case "flag":
		inv.NumFlag++
	}
}

// SplitFQDN unpacks Coolify's comma-separated fqdn field into a list
// of "host" values (no scheme). Returns nil for empty strings.
func SplitFQDN(fqdn string) []string {
	if fqdn == "" {
		return nil
	}
	parts := strings.Split(fqdn, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Strip scheme.
		p = strings.TrimPrefix(p, "https://")
		p = strings.TrimPrefix(p, "http://")
		// Strip path.
		if i := strings.Index(p, "/"); i >= 0 {
			p = p[:i]
		}
		out = append(out, p)
	}
	return out
}
