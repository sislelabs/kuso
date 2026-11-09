package projects

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"time"
)

// rolloutBatch collects the env-CR writes that the env-var and patch
// paths would otherwise issue one by one. Every write to an env CR is a
// separate helm-operator reconcile and, for podspec fields, a separate
// Deployment rollout (the operator does not coalesce), so staging three
// variables and a port change used to roll the pods two or three times.
// Inside a batch, propagateChangedToEnvs and bumpSecretsRevForService
// only record what changed; flush then writes each env CR once.
type rolloutBatch struct {
	mu         sync.Mutex
	changed    changedFields
	secretsRev bool
}

type rolloutBatchKey struct{}

func withRolloutBatch(ctx context.Context) (context.Context, *rolloutBatch) {
	b := &rolloutBatch{}
	return context.WithValue(ctx, rolloutBatchKey{}, b), b
}

// withoutRolloutBatch masks an enclosing batch so the flush itself writes.
func withoutRolloutBatch(ctx context.Context) context.Context {
	return context.WithValue(ctx, rolloutBatchKey{}, (*rolloutBatch)(nil))
}

func rolloutBatchFrom(ctx context.Context) *rolloutBatch {
	b, _ := ctx.Value(rolloutBatchKey{}).(*rolloutBatch)
	return b
}

// record ORs c into the batch. Reflective for the same reason as
// changedFields.any: a hand-written list would silently drop new fields.
func (b *rolloutBatch) record(c changedFields) {
	b.mu.Lock()
	defer b.mu.Unlock()
	dst := reflect.ValueOf(&b.changed).Elem()
	src := reflect.ValueOf(c)
	for i := 0; i < src.NumField(); i++ {
		if src.Field(i).Kind() == reflect.Bool && src.Field(i).Bool() {
			dst.Field(i).SetBool(true)
		}
	}
}

func (b *rolloutBatch) markSecretsRev() {
	b.mu.Lock()
	b.secretsRev = true
	b.mu.Unlock()
}

// ServiceChangeEnvSet is one staged variable write. The server picks the
// storage exactly as the per-key PUT with auto:true does.
type ServiceChangeEnvSet struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ServiceChangesRequest is a set of staged edits for one service, applied
// together so the running envs roll at most once.
type ServiceChangesRequest struct {
	EnvSet   []ServiceChangeEnvSet `json:"envSet,omitempty"`
	EnvUnset []string              `json:"envUnset,omitempty"`
	Patch    *PatchServiceRequest  `json:"patch,omitempty"`
	// Message is the user's note for the audit log; not stored on the CR.
	Message string `json:"message,omitempty"`
}

// ServiceChangeResult reports one item. Kind is "env-set", "env-unset" or
// "patch"; Name is the variable name (empty for the patch).
type ServiceChangeResult struct {
	Kind  string `json:"kind"`
	Name  string `json:"name,omitempty"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Err keeps the typed error for the handler's status mapping.
	Err error `json:"-"`
}

// ServiceChangesResult is the per-item outcome plus whether the single
// rollout write succeeded. Items that failed are left unapplied; the ones
// that succeeded are live and rolled out together.
type ServiceChangesResult struct {
	Results    []ServiceChangeResult `json:"results"`
	RolledOut  bool                  `json:"rolledOut"`
	FlushError string                `json:"flushError,omitempty"`
}

// ApplyServiceChanges applies variable writes, variable removals and a
// spec patch for one service, then writes each owned env CR once. A
// failed item doesn't stop the others: the caller gets per-item results
// and can retry just the failures. Env-CR writes outside propagation
// (attaching the managed Secret on first use, clearing a literal that
// moved into secret storage) still happen inline; both are no-ops in
// the steady state.
func (s *Service) ApplyServiceChanges(ctx context.Context, project, service string, req ServiceChangesRequest) (*ServiceChangesResult, error) {
	if len(req.EnvSet) == 0 && len(req.EnvUnset) == 0 && req.Patch == nil {
		return nil, fmt.Errorf("%w: no changes", ErrInvalid)
	}
	// Fail fast on a missing service instead of N identical item errors.
	if _, err := s.GetService(ctx, project, service); err != nil {
		return nil, err
	}
	bctx, batch := withRolloutBatch(ctx)
	out := &ServiceChangesResult{}
	add := func(kind, name string, err error) {
		r := ServiceChangeResult{Kind: kind, Name: name, OK: err == nil, Err: err}
		if err != nil {
			r.Error = err.Error()
		}
		out.Results = append(out.Results, r)
	}
	for _, e := range req.EnvSet {
		_, err := s.SetEnvValue(bctx, project, service, e.Name, e.Value)
		add("env-set", e.Name, err)
	}
	for _, name := range req.EnvUnset {
		_, err := s.UnsetEnvVar(bctx, project, service, name)
		if errors.Is(err, ErrNotFound) {
			err = nil // already gone: the desired end state
		}
		add("env-unset", name, err)
	}
	if req.Patch != nil {
		_, err := s.PatchService(bctx, project, service, *req.Patch)
		add("patch", "", err)
	}
	if err := s.flushRolloutBatch(ctx, project, service, batch); err != nil {
		out.FlushError = err.Error()
	} else {
		batch.mu.Lock()
		out.RolledOut = batch.changed.any() || batch.secretsRev
		batch.mu.Unlock()
	}
	return out, nil
}

// flushRolloutBatch writes every owned env CR once with the union of the
// recorded changes and, when any secret value moved, a fresh secretsRev in
// the same update.
func (s *Service) flushRolloutBatch(ctx context.Context, project, service string, b *rolloutBatch) error {
	b.mu.Lock()
	changed := b.changed
	if b.secretsRev {
		changed.SecretsRev = strconv.FormatInt(time.Now().UnixMilli(), 10)
	}
	b.mu.Unlock()
	if !changed.any() && changed.SecretsRev == "" {
		return nil
	}
	ctx = withoutRolloutBatch(ctx)
	mu := s.lockService(project, service)
	defer mu.Unlock()
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return err
	}
	svc, err := s.GetService(ctx, project, service)
	if err != nil {
		return err
	}
	return s.propagateChangedToEnvs(ctx, ns, project, service, svc, changed)
}
