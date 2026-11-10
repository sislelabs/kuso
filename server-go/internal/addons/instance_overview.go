package addons

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kuso/server/internal/kube"
)

// InstanceOverview is what the shared-database settings page shows for
// one instance Postgres: the server itself, every database on it, and
// which project, addon and services each database belongs to.
type InstanceOverview struct {
	Name      string             `json:"name"`
	Version   string             `json:"version"`
	StartedAt string             `json:"startedAt,omitempty"`
	SizeBytes int64              `json:"sizeBytes"`
	MaxConns  int                `json:"maxConnections"`
	Conns     int                `json:"connections"`
	Databases []InstanceDatabase `json:"databases"`
	// Addons that point at this server but whose database isn't on it.
	Missing []InstanceAddonRef `json:"missing"`
	// CheckedAt is when the server was read; the result is cached briefly.
	CheckedAt string `json:"checkedAt,omitempty"`
}

type InstanceDatabase struct {
	Name        string `json:"name"`
	SizeBytes   int64  `json:"sizeBytes"`
	Connections int    `json:"connections"`
	// ConnectionLimit is the owning role's CONNECTION LIMIT; -1 = none.
	ConnectionLimit int `json:"connectionLimit"`
	// "addon" = backs a kuso addon; "retained" = its addon was deleted but
	// the data and conn Secret were kept, and re-adding the addon reattaches
	// it; "orphan" = kuso created it and nothing references it any more;
	// "other" = not created by kuso (postgres, the admin's own databases).
	Kind    string `json:"kind"`
	Project string `json:"project,omitempty"`
	Addon   string `json:"addon,omitempty"`
	// AddonCR is the addon's resource name, what the project page opens.
	AddonCR  string   `json:"addonCr,omitempty"`
	Services []string `json:"services"`
}

type InstanceAddonRef struct {
	Project  string `json:"project"`
	Addon    string `json:"addon"`
	Database string `json:"database"`
}

// instanceDBRow is one database as Postgres reports it.
type instanceDBRow struct {
	Name        string
	SizeBytes   int64
	Connections int
	ConnLimit   int
	Comment     string
}

// overviewTTL bounds how often the page's poll reaches Postgres: each read
// opens an admin connection and sizes every database.
const overviewTTL = 60 * time.Second

type overviewEntry struct {
	mu  sync.Mutex
	at  time.Time
	out *InstanceOverview
}

// InstanceOverview reads the named instance Postgres with the admin DSN,
// at most once per overviewTTL. Read-only: catalog and statistics views.
func (s *Service) InstanceOverview(ctx context.Context, name string) (*InstanceOverview, error) {
	v, _ := s.overviewCache.LoadOrStore(name, &overviewEntry{})
	e := v.(*overviewEntry)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.out != nil && time.Since(e.at) < overviewTTL {
		return e.out, nil
	}
	out, err := s.readInstanceOverview(ctx, name)
	if err != nil {
		return nil, err
	}
	e.out, e.at = out, time.Now()
	return out, nil
}

func (s *Service) readInstanceOverview(ctx context.Context, name string) (*InstanceOverview, error) {
	dsn, err := s.instanceAdminDSN(ctx, name)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open instance %s: %w", name, err)
	}
	defer db.Close()

	out := &InstanceOverview{Name: name, CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	var started sql.NullTime
	var maxConns string
	if err := db.QueryRowContext(ctx,
		`SELECT current_setting('server_version'), pg_postmaster_start_time(), current_setting('max_connections')`,
	).Scan(&out.Version, &started, &maxConns); err != nil {
		return nil, fmt.Errorf("read instance %s: %w", name, err)
	}
	if started.Valid {
		out.StartedAt = started.Time.UTC().Format(time.RFC3339)
	}
	if out.MaxConns, err = strconv.Atoi(strings.TrimSpace(maxConns)); err != nil {
		return nil, fmt.Errorf("read instance %s: max_connections %q: %w", name, maxConns, err)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT d.datname, pg_database_size(d.oid), COALESCE(a.n, 0), COALESCE(r.rolconnlimit, -1),
		       COALESCE(shobj_description(d.oid, 'pg_database'), '')
		  FROM pg_database d
		  LEFT JOIN (SELECT datname, count(*) AS n FROM pg_stat_activity
		              WHERE backend_type = 'client backend' GROUP BY datname) a
		         ON a.datname = d.datname
		  LEFT JOIN pg_roles r ON r.rolname = d.datname
		 WHERE NOT d.datistemplate`)
	if err != nil {
		return nil, fmt.Errorf("list databases on %s: %w", name, err)
	}
	defer rows.Close()
	var dbs []instanceDBRow
	for rows.Next() {
		var r instanceDBRow
		if err := rows.Scan(&r.Name, &r.SizeBytes, &r.Connections, &r.ConnLimit, &r.Comment); err != nil {
			return nil, fmt.Errorf("scan database row: %w", err)
		}
		dbs = append(dbs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list databases on %s: %w", name, err)
	}

	addonList, err := s.Kube.ListKusoAddons(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list addons: %w", err)
	}
	envs, err := s.Kube.ListKusoEnvironments(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	// The database an addon's app actually uses is the one its conn Secret
	// names; re-deriving it from the addon name gets the DATA-02 case wrong
	// and would list a live database as an orphan.
	connDB := map[string]string{}
	for i := range addonList {
		a := &addonList[i]
		if a.Spec.UseInstanceAddon != name {
			continue
		}
		if v := string(s.connSecretData(ctx, a.Namespace, connSecretName(a.Name))["POSTGRES_DB"]); v != "" {
			connDB[a.Name] = v
		}
	}
	buildInstanceOverview(out, dbs, addonList, envs, connDB)
	// A stamped database whose conn Secret survived its addon's delete is
	// retained, not abandoned: re-adding the addon reattaches it.
	markRetained(out, func(project, addon string) string {
		return string(s.connSecretData(ctx, s.nsFor(ctx, project), connSecretName(CRName(project, addon)))["POSTGRES_DB"])
	})
	return out, nil
}

// buildInstanceOverview maps each database to the addon whose conn Secret
// names it (connDB: addon CR -> POSTGRES_DB; the derived identifier when
// absent) and the services that consume that conn Secret, through
// envFromSecrets or a secretKeyRef (`${{ db.DATABASE_URL }}`).
func buildInstanceOverview(out *InstanceOverview, dbs []instanceDBRow, addonList []kube.KusoAddon, envs []kube.KusoEnvironment, connDB map[string]string) {
	type owner struct{ project, addonCR, short string }
	byIdent := map[string]owner{}
	for i := range addonList {
		a := &addonList[i]
		if a.Spec.UseInstanceAddon != out.Name {
			continue
		}
		p := a.Spec.Project
		if p == "" {
			p = a.Labels[kube.LabelProject]
		}
		short := instanceAddonName(a, p)
		ident := connDB[a.Name]
		if ident == "" {
			ident = pgIdentifier(p, short)
		}
		byIdent[ident] = owner{project: p, addonCR: a.Name, short: short}
	}

	usersOf := map[string]map[string]bool{} // conn secret -> service short names
	use := func(sec, svc string) {
		if usersOf[sec] == nil {
			usersOf[sec] = map[string]bool{}
		}
		usersOf[sec][svc] = true
	}
	for i := range envs {
		e := &envs[i]
		svc := strings.TrimPrefix(e.Spec.Service, e.Spec.Project+"-")
		for _, sec := range e.Spec.EnvFromSecrets {
			use(sec, svc)
		}
		for _, v := range e.Spec.EnvVars {
			ref, _ := v.ValueFrom["secretKeyRef"].(map[string]any)
			if n, _ := ref["name"].(string); n != "" {
				use(n, svc)
			}
		}
	}

	seen := map[string]bool{}
	out.Databases = make([]InstanceDatabase, 0, len(dbs))
	for _, r := range dbs {
		d := InstanceDatabase{Name: r.Name, SizeBytes: r.SizeBytes, Connections: r.Connections, ConnectionLimit: r.ConnLimit, Services: []string{}}
		out.SizeBytes += r.SizeBytes
		out.Conns += r.Connections
		if o, ok := byIdent[r.Name]; ok {
			seen[r.Name] = true
			d.Kind, d.Project, d.Addon, d.AddonCR = "addon", o.project, o.short, o.addonCR
			for svc := range usersOf[ConnSecretName(o.addonCR)] {
				d.Services = append(d.Services, svc)
			}
			sort.Strings(d.Services)
		} else if strings.HasPrefix(r.Comment, instanceDBOwnerPrefix) {
			d.Kind = "orphan"
			if p, a, ok := strings.Cut(strings.TrimPrefix(r.Comment, instanceDBOwnerPrefix), "/"); ok {
				d.Project, d.Addon = p, a
			}
		} else {
			d.Kind = "other"
		}
		out.Databases = append(out.Databases, d)
	}
	sort.SliceStable(out.Databases, func(i, j int) bool { return out.Databases[i].SizeBytes > out.Databases[j].SizeBytes })

	out.Missing = []InstanceAddonRef{}
	for ident, o := range byIdent {
		if !seen[ident] {
			out.Missing = append(out.Missing, InstanceAddonRef{Project: o.project, Addon: o.short, Database: ident})
		}
	}
	sort.Slice(out.Missing, func(i, j int) bool { return out.Missing[i].Database < out.Missing[j].Database })
}

// markRetained reclassifies an orphan as retained when the conn Secret of
// the addon its stamp names still points at it. connDBOf returns that
// Secret's POSTGRES_DB ("" when there is none).
func markRetained(out *InstanceOverview, connDBOf func(project, addon string) string) {
	for i := range out.Databases {
		d := &out.Databases[i]
		if d.Kind != "orphan" || d.Project == "" || d.Addon == "" {
			continue
		}
		if connDBOf(d.Project, d.Addon) == d.Name {
			d.Kind = "retained"
		}
	}
}
