// Package podsizes owns the pod-size presets' defaults: the presets seeded
// on an empty table, the instance-wide "default pod size" setting, and the
// preset → spec.resources translation new services get.
package podsizes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"kuso/server/internal/db"
)

// SettingKey is the Setting row holding the default preset's name.
const SettingKey = "defaultPodSize"

// FallbackName is the default preset when SettingKey is unset.
const FallbackName = "medium"

// None disables the default: new services get no resources.
const None = "none"

// ErrUnknownPreset is returned by SetDefault for a name no preset has.
var ErrUnknownPreset = errors.New("unknown pod size")

// Store is the slice of *db.DB this package needs.
type Store interface {
	ListPodSizes(ctx context.Context) ([]db.PodSize, error)
	CreatePodSize(ctx context.Context, p *db.PodSize) error
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value, updatedBy string) error
}

// Presets are seeded into an empty PodSize table. No CPU limit on any of
// them: a CPU limit throttles, while the request is what the scheduler
// needs to place pods. Deterministic IDs make a concurrent seed from a
// second replica collide on the primary key instead of duplicating rows.
var Presets = []db.PodSize{
	{ID: "podsize-small", Name: "small", CPURequest: "50m", MemoryRequest: "128Mi", MemoryLimit: "512Mi",
		Description: sql.NullString{String: "light apps and workers", Valid: true}},
	{ID: "podsize-medium", Name: "medium", CPURequest: "100m", MemoryRequest: "256Mi", MemoryLimit: "1Gi",
		Description: sql.NullString{String: "default for most web apps", Valid: true}},
	{ID: "podsize-large", Name: "large", CPURequest: "250m", MemoryRequest: "512Mi", MemoryLimit: "2Gi",
		Description: sql.NullString{String: "memory-heavy apps", Valid: true}},
}

// Seed inserts Presets when the table is empty and returns how many rows
// it wrote. A table with any row is left untouched.
func Seed(ctx context.Context, st Store) (int, error) {
	existing, err := st.ListPodSizes(ctx)
	if err != nil {
		return 0, fmt.Errorf("podsizes: seed: %w", err)
	}
	if len(existing) > 0 {
		return 0, nil
	}
	n := 0
	for i := range Presets {
		p := Presets[i]
		if err := st.CreatePodSize(ctx, &p); err != nil {
			return n, fmt.Errorf("podsizes: seed %s: %w", p.Name, err)
		}
		n++
	}
	return n, nil
}

// DefaultName returns the configured default preset name, FallbackName when
// unset, or None.
func DefaultName(ctx context.Context, st Store) (string, error) {
	v, err := st.GetSetting(ctx, SettingKey)
	if err != nil {
		return "", fmt.Errorf("podsizes: read default: %w", err)
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return FallbackName, nil
	}
	return v, nil
}

// SetDefault stores name as the default preset. name must be None or an
// existing preset's name.
func SetDefault(ctx context.Context, st Store, name, actor string) error {
	name = strings.TrimSpace(name)
	if name != None {
		if _, ok, err := find(ctx, st, name); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("%w: %q", ErrUnknownPreset, name)
		}
	}
	if err := st.SetSetting(ctx, SettingKey, name, actor); err != nil {
		return fmt.Errorf("podsizes: set default: %w", err)
	}
	return nil
}

// DefaultResources is the default preset's spec.resources map, or nil when
// the default is None or names a preset that no longer exists.
func DefaultResources(ctx context.Context, st Store) (map[string]any, error) {
	name, err := DefaultName(ctx, st)
	if err != nil || name == None {
		return nil, err
	}
	p, ok, err := find(ctx, st, name)
	if err != nil || !ok {
		return nil, err
	}
	return Resources(p), nil
}

// Resources renders a preset as the k8s ResourceRequirements map the
// kusoenvironment chart passes through toYaml. Empty quantities are
// omitted; a preset with none set yields nil.
func Resources(p db.PodSize) map[string]any {
	out := map[string]any{}
	add := func(section, key, qty string) {
		if qty = strings.TrimSpace(qty); qty == "" {
			return
		}
		m, _ := out[section].(map[string]any)
		if m == nil {
			m = map[string]any{}
			out[section] = m
		}
		m[key] = qty
	}
	add("requests", "cpu", p.CPURequest)
	add("requests", "memory", p.MemoryRequest)
	add("limits", "cpu", p.CPULimit)
	add("limits", "memory", p.MemoryLimit)
	if len(out) == 0 {
		return nil
	}
	return out
}

func find(ctx context.Context, st Store, name string) (db.PodSize, bool, error) {
	sizes, err := st.ListPodSizes(ctx)
	if err != nil {
		return db.PodSize{}, false, fmt.Errorf("podsizes: list: %w", err)
	}
	for _, p := range sizes {
		if p.Name == name {
			return p, true, nil
		}
	}
	return db.PodSize{}, false, nil
}
