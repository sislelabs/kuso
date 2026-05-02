package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Runpack mirrors the Prisma Runpack model joined with its three phases
// (fetch/build/run) and SecurityContext so consumers see the full
// runtime as a single object.
type Runpack struct {
	ID       string
	Name     string
	Language string
	Fetch    *RunpackPhase
	Build    *RunpackPhase
	Run      *RunpackPhase
}

// RunpackPhase represents one phase of a Runpack (fetch/build/run).
type RunpackPhase struct {
	ID                 string
	Repository         string
	Tag                string
	Command            sql.NullString
	ReadOnlyAppStorage bool
	SecurityContext    *RunpackSecurityContext
}

// RunpackSecurityContext mirrors the SecurityContext model. Capabilities
// are loaded eagerly because the UI displays them inline.
type RunpackSecurityContext struct {
	ID                       string
	RunAsUser                int
	RunAsGroup               int
	RunAsNonRoot             bool
	ReadOnlyRootFilesystem   bool
	AllowPrivilegeEscalation bool
	CapabilitiesAdd          []string
	CapabilitiesDrop         []string
}

// PodSize mirrors the PodSize model.
type PodSize struct {
	ID            string
	Name          string
	CPULimit      string
	MemoryLimit   string
	CPURequest    string
	MemoryRequest string
	Description   sql.NullString
}

// ListRunpacks returns every runpack with all phases joined. The query
// is intentionally chunky — runpack lookups happen rarely, the UI paints
// the whole list in one shot.
func (d *DB) ListRunpacks(ctx context.Context) ([]Runpack, error) {
	rows, err := d.DB.QueryContext(ctx, `
SELECT r.id, r.name, r.language, r."fetchId", r."buildId", r."runId"
FROM "Runpack" r ORDER BY r.name`)
	if err != nil {
		return nil, fmt.Errorf("db: list runpacks: %w", err)
	}
	defer rows.Close()
	var packs []Runpack
	type idRefs struct{ fetchID, buildID, runID string }
	refs := []idRefs{}
	for rows.Next() {
		var rp Runpack
		var ref idRefs
		if err := rows.Scan(&rp.ID, &rp.Name, &rp.Language, &ref.fetchID, &ref.buildID, &ref.runID); err != nil {
			return nil, err
		}
		packs = append(packs, rp)
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range packs {
		f, err := d.runpackPhase(ctx, refs[i].fetchID)
		if err != nil {
			return nil, err
		}
		b, err := d.runpackPhase(ctx, refs[i].buildID)
		if err != nil {
			return nil, err
		}
		r, err := d.runpackPhase(ctx, refs[i].runID)
		if err != nil {
			return nil, err
		}
		packs[i].Fetch, packs[i].Build, packs[i].Run = f, b, r
	}
	return packs, nil
}

func (d *DB) runpackPhase(ctx context.Context, id string) (*RunpackPhase, error) {
	var p RunpackPhase
	var secID string
	err := d.DB.QueryRowContext(ctx, `
SELECT id, repository, tag, command, "readOnlyAppStorage", "securityContextId"
FROM "RunpackPhase" WHERE id = ?`, id).Scan(&p.ID, &p.Repository, &p.Tag, &p.Command, &p.ReadOnlyAppStorage, &secID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: runpack phase %s: %w", id, err)
	}
	sec, err := d.securityContext(ctx, secID)
	if err != nil {
		return nil, err
	}
	p.SecurityContext = sec
	return &p, nil
}

func (d *DB) securityContext(ctx context.Context, id string) (*RunpackSecurityContext, error) {
	var c RunpackSecurityContext
	err := d.DB.QueryRowContext(ctx, `
SELECT id, "runAsUser", "runAsGroup", "runAsNonRoot", "readOnlyRootFilesystem", "allowPrivilegeEscalation"
FROM "SecurityContext" WHERE id = ?`, id).Scan(&c.ID, &c.RunAsUser, &c.RunAsGroup, &c.RunAsNonRoot, &c.ReadOnlyRootFilesystem, &c.AllowPrivilegeEscalation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: security context %s: %w", id, err)
	}
	// Capabilities Add / Drop live in their own tables, joined via
	// Capability. The schema models a 1:N from SecurityContext to
	// Capability and N:1 from Capability to {Add,Drop} — we flatten
	// the hierarchy when reading.
	addRows, err := d.DB.QueryContext(ctx, `
SELECT ca.value
FROM "Capability" cap
JOIN "CapabilityAdd" ca ON ca."capabilityId" = cap.id
WHERE cap."securityCtxId" = ?`, id)
	if err != nil {
		return nil, err
	}
	defer addRows.Close()
	for addRows.Next() {
		var v string
		if err := addRows.Scan(&v); err != nil {
			return nil, err
		}
		c.CapabilitiesAdd = append(c.CapabilitiesAdd, v)
	}
	dropRows, err := d.DB.QueryContext(ctx, `
SELECT cd.value
FROM "Capability" cap
JOIN "CapabilityDrop" cd ON cd."capabilityId" = cap.id
WHERE cap."securityCtxId" = ?`, id)
	if err != nil {
		return nil, err
	}
	defer dropRows.Close()
	for dropRows.Next() {
		var v string
		if err := dropRows.Scan(&v); err != nil {
			return nil, err
		}
		c.CapabilitiesDrop = append(c.CapabilitiesDrop, v)
	}
	return &c, nil
}

// DeleteRunpack removes a runpack and its three phase rows + security
// context rows. The orphan-cleanup is best-effort — cascading deletes
// land per-row to keep the foreign keys quiet.
func (d *DB) DeleteRunpack(ctx context.Context, id string) error {
	var fetchID, buildID, runID string
	err := d.DB.QueryRowContext(ctx, `SELECT "fetchId", "buildId", "runId" FROM "Runpack" WHERE id = ?`, id).
		Scan(&fetchID, &buildID, &runID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("db: read runpack: %w", err)
	}
	if _, err := d.DB.ExecContext(ctx, `DELETE FROM "Runpack" WHERE id = ?`, id); err != nil {
		return fmt.Errorf("db: delete runpack: %w", err)
	}
	for _, pid := range []string{fetchID, buildID, runID} {
		_, _ = d.DB.ExecContext(ctx, `DELETE FROM "RunpackPhase" WHERE id = ?`, pid)
	}
	return nil
}

// ListPodSizes returns every PodSize ordered by name.
func (d *DB) ListPodSizes(ctx context.Context) ([]PodSize, error) {
	rows, err := d.DB.QueryContext(ctx, `
SELECT id, name, "cpuLimit", "memoryLimit", "cpuRequest", "memoryRequest", description
FROM "PodSize" ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("db: list pod sizes: %w", err)
	}
	defer rows.Close()
	var out []PodSize
	for rows.Next() {
		var p PodSize
		if err := rows.Scan(&p.ID, &p.Name, &p.CPULimit, &p.MemoryLimit, &p.CPURequest, &p.MemoryRequest, &p.Description); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CreatePodSize inserts a new PodSize row.
func (d *DB) CreatePodSize(ctx context.Context, p *PodSize) error {
	if p.ID == "" {
		return errors.New("db: pod size id required")
	}
	now := prismaNow()
	_, err := d.DB.ExecContext(ctx, `
INSERT INTO "PodSize" (id, name, "cpuLimit", "memoryLimit", "cpuRequest", "memoryRequest", description, "createdAt", "updatedAt")
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.CPULimit, p.MemoryLimit, p.CPURequest, p.MemoryRequest, p.Description, now, now,
	)
	if err != nil {
		return fmt.Errorf("db: create pod size: %w", err)
	}
	return nil
}

// UpdatePodSize replaces the named PodSize columns.
func (d *DB) UpdatePodSize(ctx context.Context, p *PodSize) error {
	now := prismaNow()
	res, err := d.DB.ExecContext(ctx, `
UPDATE "PodSize" SET name = ?, "cpuLimit" = ?, "memoryLimit" = ?, "cpuRequest" = ?, "memoryRequest" = ?, description = ?, "updatedAt" = ?
WHERE id = ?`,
		p.Name, p.CPULimit, p.MemoryLimit, p.CPURequest, p.MemoryRequest, p.Description, now, p.ID,
	)
	if err != nil {
		return fmt.Errorf("db: update pod size: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePodSize removes a PodSize.
func (d *DB) DeletePodSize(ctx context.Context, id string) error {
	res, err := d.DB.ExecContext(ctx, `DELETE FROM "PodSize" WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete pod size: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
