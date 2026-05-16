package handlers

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/addons"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
	"kuso/server/internal/projectsecrets"
	"kuso/server/internal/version"
)

// ExportHandler streams a project's full spec (project + services +
// envs + addons + secrets) as a tar.gz over a single HTTP response.
//
// Scope: this is a SPEC export. Addon data (postgres rows, redis
// keys, S3 objects) is NOT included — moving live data is a separate
// concern handled by `kuso addon-backup`. Including pg_dump inside an
// HTTP response would block the request for the duration of the dump
// and hold the whole archive in memory; the addon-backup tooling
// streams to S3 directly and is the right primitive for data motion.
//
// The export is deterministic enough for `diff` to be useful between
// two snapshots taken in quick succession, but timestamps in the
// underlying CRs will of course drift across them.
type ExportHandler struct {
	Projects       *projects.Service
	Addons         *addons.Service
	ProjectSecrets *projectsecrets.Service
	Kube           *kube.Client
	NSResolver     *kube.ProjectNamespaceResolver
	Namespace      string
	DB             *db.DB
	Logger         *slog.Logger
}

// Mount registers POST /api/projects/{project}/export on the bearer
// router. POST because it produces a side effect (audit log entry)
// and to mirror the /import endpoint. Also wires POST
// /api/projects/import to ingest a tarball produced by Export.
func (h *ExportHandler) Mount(r chi.Router) {
	r.Post("/api/projects/{project}/export", h.Export)
	r.Post("/api/projects/import", h.Import)
}

// exportManifest is the top-level JSON written to manifest.json in
// every export tarball. version is the source instance's kuso build;
// schema is the export format version (bump on breaking changes).
type exportManifest struct {
	Schema      int       `json:"schema"`
	KusoVersion string    `json:"kusoVersion"`
	ExportedAt  time.Time `json:"exportedAt"`
	Project     string    `json:"project"`
	// Counts so the importer can sanity-check it isn't truncated.
	Services     int `json:"services"`
	Environments int `json:"environments"`
	Addons       int `json:"addons"`
	// IncludesAddonData is always false today — the field is here so
	// future versions that DO bundle pg_dump can flip it without a
	// schema bump.
	IncludesAddonData bool `json:"includesAddonData"`
}

// Export writes a tar.gz with the project's spec + secrets. See the
// package comment on this type for what's in scope.
func (h *ExportHandler) Export(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	if project == "" {
		http.Error(w, "missing project", http.StatusBadRequest)
		return
	}
	// Export reads everything in the project including the resolved
	// secret values, so gate on Deployer-or-higher. Viewer would also
	// be defensible but secret values are the kind of thing a viewer
	// shouldn't be able to siphon out in one round-trip.
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleDeployer) {
		return
	}
	if h.Projects == nil || h.Addons == nil || h.Kube == nil {
		http.Error(w, "export not wired", http.StatusServiceUnavailable)
		return
	}

	// Pull spec data up-front so we can include accurate counts in
	// the manifest. The describe path uses the projects.Service's
	// own caches so this is cheap.
	desc, err := h.Projects.Describe(ctx, project)
	if err != nil {
		if err == projects.ErrNotFound {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("export: describe", "project", project, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	addonList, err := h.Addons.List(ctx, project)
	if err != nil {
		h.Logger.Error("export: list addons", "project", project, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}

	manifest := exportManifest{
		Schema:       1,
		KusoVersion:  version.Version(),
		ExportedAt:   time.Now().UTC(),
		Project:      project,
		Services:     len(desc.Services),
		Environments: len(desc.Environments),
		Addons:       len(addonList),
	}

	filename := fmt.Sprintf("kuso-export-%s-%s.tar.gz", project, time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	// We can't surface a 500 cleanly once the response has started
	// streaming — log and close gracefully so the client at least
	// gets a syntactically-valid (if short) tar.
	defer func() {
		if err := tw.Close(); err != nil {
			h.Logger.Warn("export: tar close", "project", project, "err", err)
		}
		if err := gz.Close(); err != nil {
			h.Logger.Warn("export: gzip close", "project", project, "err", err)
		}
	}()

	writeFile := func(path string, body []byte) error {
		hdr := &tar.Header{
			Name:    path,
			Size:    int64(len(body)),
			Mode:    0o600,
			ModTime: time.Now().UTC(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tw.Write(body)
		return err
	}
	writeJSON := func(path string, v any) error {
		body, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		return writeFile(path, body)
	}

	if err := writeJSON("manifest.json", manifest); err != nil {
		h.Logger.Error("export: write manifest", "err", err)
		return
	}
	if err := writeJSON("project.json", desc.Project); err != nil {
		h.Logger.Error("export: write project", "err", err)
		return
	}
	// Strip server-only metadata so re-import doesn't accidentally
	// preserve managedFields / resourceVersion that would be wrong on
	// the destination cluster. We keep .spec and .metadata.{name,
	// labels, annotations} which is all the importer needs.
	for i := range desc.Services {
		stripServerMeta(&desc.Services[i].ObjectMeta)
		if err := writeJSON(fmt.Sprintf("services/%s.json", desc.Services[i].Name), desc.Services[i]); err != nil {
			h.Logger.Error("export: write service", "err", err)
			return
		}
	}
	for i := range desc.Environments {
		stripServerMeta(&desc.Environments[i].ObjectMeta)
		if err := writeJSON(fmt.Sprintf("envs/%s.json", desc.Environments[i].Name), desc.Environments[i]); err != nil {
			h.Logger.Error("export: write env", "err", err)
			return
		}
	}
	for i := range addonList {
		stripServerMeta(&addonList[i].ObjectMeta)
		if err := writeJSON(fmt.Sprintf("addons/%s.json", addonList[i].Name), addonList[i]); err != nil {
			h.Logger.Error("export: write addon", "err", err)
			return
		}
	}

	// Shared project secret (the "instance secrets attached to every
	// env in this project" surface). Optional: if the Secret doesn't
	// exist, just skip the file.
	if h.ProjectSecrets != nil {
		ns := h.nsFor(ctx, project)
		if sec, err := h.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, projectsecrets.SecretName(project), metav1.GetOptions{}); err == nil {
			data := map[string]string{}
			for k, v := range sec.Data {
				data[k] = string(v)
			}
			if err := writeJSON("secrets/project.json", data); err != nil {
				h.Logger.Error("export: write project secret", "err", err)
				return
			}
		} else if !apierrors.IsNotFound(err) {
			h.Logger.Warn("export: project secret read", "err", err)
		}
	}

	// Per-env Secrets. Convention: name is "<env>-secrets" in the
	// project's namespace. We enumerate them via the env CR list +
	// fetch each one's Secret by name.
	ns := h.nsFor(ctx, project)
	for _, env := range desc.Environments {
		secName := env.Name + "-secrets"
		sec, err := h.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, secName, metav1.GetOptions{})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				h.Logger.Warn("export: env secret read", "env", env.Name, "err", err)
			}
			continue
		}
		data := map[string]string{}
		for k, v := range sec.Data {
			data[k] = string(v)
		}
		if err := writeJSON(fmt.Sprintf("secrets/envs/%s.json", env.Name), data); err != nil {
			h.Logger.Error("export: write env secret", "env", env.Name, "err", err)
			return
		}
	}

	h.Logger.Info("project exported",
		"project", project,
		"services", manifest.Services,
		"envs", manifest.Environments,
		"addons", manifest.Addons,
	)
}

// importResult is what /api/projects/import returns on success.
type importResult struct {
	Project      string   `json:"project"`
	Services     int      `json:"services"`
	Environments int      `json:"environments"`
	Addons       int      `json:"addons"`
	Secrets      int      `json:"secrets"`
	Warnings     []string `json:"warnings,omitempty"`
}

// Import ingests a tar.gz produced by Export and recreates the
// project + services + envs + addons + secrets on this cluster.
//
// Body: the raw tar.gz from Export (Content-Type doesn't matter; we
// sniff gzip magic).
//
// Query params:
//
//	policy=error    — refuse if the project name already exists (default)
//	policy=rename   — auto-suffix the project name on conflict, e.g.
//	                  "shop" → "shop-imported-20260512-1530"
//	policy=overwrite — delete the existing project first (DESTRUCTIVE)
//
// Domains in the spec are preserved. The handler does NOT warn on
// host clashes during ingest — the user explicitly asked to keep
// domains and we trust their judgement. Operators about to import
// over the same cluster's existing project should use policy=rename
// or policy=overwrite, not error.
//
// Admin-only because the import creates resources across the whole
// project namespace and bypasses the per-resource Deployer gate.
func (h *ExportHandler) Import(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if h.Projects == nil || h.Kube == nil {
		http.Error(w, "import not wired", http.StatusServiceUnavailable)
		return
	}
	// Long deadline — recreating ~20 CRs across the operator's
	// 60s reconcile cadence can take a couple of minutes. The
	// individual kube writes are sub-second; this is mostly for
	// the operator handshake on each helm release.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	// Cap upload size at 16 MiB. A spec-only export is typically
	// tens of KB; 16 MiB is room for an unusually-busy project
	// while bounding the memory we'll hold.
	body := http.MaxBytesReader(w, r.Body, 16<<20)
	defer body.Close()

	gz, err := gzip.NewReader(body)
	if err != nil {
		http.Error(w, "expected gzip-compressed tar (Export output)", http.StatusBadRequest)
		return
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	// First pass: load every file into memory keyed by path. Two-pass
	// is required because the manifest can appear anywhere in the
	// archive; rewinding a tar stream isn't free and the spec data
	// is small enough that in-memory is fine.
	files := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		buf := make([]byte, hdr.Size)
		if _, err := io.ReadFull(tr, buf); err != nil {
			http.Error(w, fmt.Sprintf("tar read %s: %v", hdr.Name, err), http.StatusBadRequest)
			return
		}
		files[hdr.Name] = buf
	}

	rawManifest, ok := files["manifest.json"]
	if !ok {
		http.Error(w, "manifest.json missing — is this a kuso export tarball?", http.StatusBadRequest)
		return
	}
	var manifest exportManifest
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		http.Error(w, fmt.Sprintf("manifest decode: %v", err), http.StatusBadRequest)
		return
	}
	if manifest.Schema != 1 {
		http.Error(w, fmt.Sprintf("unsupported export schema %d (this kuso supports schema 1)", manifest.Schema), http.StatusBadRequest)
		return
	}
	if manifest.Project == "" {
		http.Error(w, "manifest has no project name", http.StatusBadRequest)
		return
	}

	rawProject, ok := files["project.json"]
	if !ok {
		http.Error(w, "project.json missing from tarball", http.StatusBadRequest)
		return
	}
	var proj kube.KusoProject
	if err := json.Unmarshal(rawProject, &proj); err != nil {
		http.Error(w, fmt.Sprintf("project decode: %v", err), http.StatusBadRequest)
		return
	}

	policy := r.URL.Query().Get("policy")
	if policy == "" {
		policy = "error"
	}
	desiredName := proj.Name
	switch policy {
	case "error", "rename", "overwrite":
	default:
		http.Error(w, "policy must be one of: error, rename, overwrite", http.StatusBadRequest)
		return
	}
	// Conflict detection.
	if existing, err := h.Projects.Get(ctx, desiredName); err == nil && existing != nil {
		switch policy {
		case "error":
			http.Error(w, fmt.Sprintf("project %q already exists; pass ?policy=rename or ?policy=overwrite", desiredName), http.StatusConflict)
			return
		case "rename":
			desiredName = fmt.Sprintf("%s-imported-%s", proj.Name, time.Now().UTC().Format("20060102-1504"))
		case "overwrite":
			// Best-effort delete via raw kube. We don't go through the
			// higher-level Service.Delete because that path enforces
			// "no production envs"; the destructive policy is the
			// operator's explicit ask.
			if err := h.Kube.DeleteKusoProject(ctx, h.Namespace, desiredName); err != nil && !apierrors.IsNotFound(err) {
				http.Error(w, fmt.Sprintf("overwrite: delete existing project: %v", err), http.StatusInternalServerError)
				return
			}
			// Give the operator a beat to finalise the helm uninstall.
			// Without this, the create below races the delete and
			// gets AlreadyExists.
			time.Sleep(2 * time.Second)
		}
	}

	// Renaming: rewrite the project name + every dependent resource
	// name that embeds the project prefix. Conservative: only the
	// project's own .metadata.name and the standard "<project>-<x>"
	// pattern on service/env/addon names. Spec-internal references
	// (project field on env/addon specs) are also rewritten.
	renameMap := map[string]string{}
	if desiredName != proj.Name {
		renameMap[proj.Name] = desiredName
		proj.Name = desiredName
	}

	out := importResult{Project: desiredName}

	// Phase 1: project.
	stripServerMeta(&proj.ObjectMeta)
	if _, err := h.Kube.CreateKusoProject(ctx, h.Namespace, &proj); err != nil {
		http.Error(w, fmt.Sprintf("create project: %v", err), http.StatusInternalServerError)
		return
	}

	ns := h.nsFor(ctx, desiredName)

	// Phase 2: services.
	for path, raw := range files {
		if !pathHasPrefix(path, "services/") || !pathHasSuffix(path, ".json") {
			continue
		}
		var s kube.KusoService
		if err := json.Unmarshal(raw, &s); err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("decode %s: %v", path, err))
			continue
		}
		applyProjectRename(&s.ObjectMeta, &s.Spec.Project, renameMap)
		stripServerMeta(&s.ObjectMeta)
		if _, err := h.Kube.CreateKusoService(ctx, ns, &s); err != nil {
			if apierrors.IsAlreadyExists(err) {
				out.Warnings = append(out.Warnings, fmt.Sprintf("service %s already exists, skipping", s.Name))
				continue
			}
			out.Warnings = append(out.Warnings, fmt.Sprintf("create service %s: %v", s.Name, err))
			continue
		}
		out.Services++
	}

	// Phase 3: envs.
	for path, raw := range files {
		if !pathHasPrefix(path, "envs/") || !pathHasSuffix(path, ".json") {
			continue
		}
		var e kube.KusoEnvironment
		if err := json.Unmarshal(raw, &e); err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("decode %s: %v", path, err))
			continue
		}
		applyProjectRename(&e.ObjectMeta, &e.Spec.Project, renameMap)
		// Service name also embeds project prefix.
		if newSvc, ok := renameMap[e.Spec.Service]; ok {
			e.Spec.Service = newSvc
		}
		stripServerMeta(&e.ObjectMeta)
		// Re-attach the ownerRef to the freshly-imported parent service
		// so kube GC continues to cascade on delete after restore.
		if parent, gerr := h.Kube.GetKusoService(ctx, ns, e.Spec.Service); gerr == nil && parent != nil {
			e.ObjectMeta.OwnerReferences = []metav1.OwnerReference{kube.OwnerRefForService(parent)}
		}
		if _, err := h.Kube.CreateKusoEnvironment(ctx, ns, &e); err != nil {
			if apierrors.IsAlreadyExists(err) {
				out.Warnings = append(out.Warnings, fmt.Sprintf("env %s already exists, skipping", e.Name))
				continue
			}
			out.Warnings = append(out.Warnings, fmt.Sprintf("create env %s: %v", e.Name, err))
			continue
		}
		out.Environments++
	}

	// Phase 4: addons.
	for path, raw := range files {
		if !pathHasPrefix(path, "addons/") || !pathHasSuffix(path, ".json") {
			continue
		}
		var a kube.KusoAddon
		if err := json.Unmarshal(raw, &a); err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("decode %s: %v", path, err))
			continue
		}
		applyProjectRename(&a.ObjectMeta, &a.Spec.Project, renameMap)
		stripServerMeta(&a.ObjectMeta)
		if _, err := h.Kube.CreateKusoAddon(ctx, ns, &a); err != nil {
			if apierrors.IsAlreadyExists(err) {
				out.Warnings = append(out.Warnings, fmt.Sprintf("addon %s already exists, skipping", a.Name))
				continue
			}
			out.Warnings = append(out.Warnings, fmt.Sprintf("create addon %s: %v", a.Name, err))
			continue
		}
		out.Addons++
	}

	// Phase 5: shared project secret.
	if raw, ok := files["secrets/project.json"]; ok && h.ProjectSecrets != nil {
		var data map[string]string
		if err := json.Unmarshal(raw, &data); err == nil {
			for k, v := range data {
				// Rolled count is irrelevant during import — every env
				// is brand new and hasn't started consuming the shared
				// Secret yet. Force=true bypasses the shadow guard
				// because import is bulk infrastructure setup; if the
				// source bundle defined the same key in both shared and
				// service-scoped, the user's intent is to recreate that
				// exact topology, and the guard would refuse legitimate
				// writes.
				if _, err := h.ProjectSecrets.SetKey(ctx, desiredName, k, v, projectsecrets.SetOptions{Force: true}); err != nil {
					out.Warnings = append(out.Warnings, fmt.Sprintf("project secret %s: %v", k, err))
				} else {
					out.Secrets++
				}
			}
		}
	}

	// Phase 6: per-env Secrets. Apply rename so env-secret names track
	// the renamed env CRs. We write the Secret directly because the
	// kuso secrets.Service expects (project, service, env) tuples and
	// not raw key=value blobs.
	for path, raw := range files {
		if !pathHasPrefix(path, "secrets/envs/") || !pathHasSuffix(path, ".json") {
			continue
		}
		envName := pathTrimPrefix(pathTrimSuffix(path, ".json"), "secrets/envs/")
		// Rename map can rewrite env names that embed the project
		// prefix. We apply the same logic the import does on env CRs:
		// if oldProject is a prefix of envName, swap it in.
		for old, new := range renameMap {
			if envName == old || pathHasPrefix(envName, old+"-") {
				envName = new + envName[len(old):]
				break
			}
		}
		var data map[string]string
		if err := json.Unmarshal(raw, &data); err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("decode %s: %v", path, err))
			continue
		}
		secName := envName + "-secrets"
		byteData := map[string][]byte{}
		for k, v := range data {
			byteData[k] = []byte(v)
		}
		sec := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secName,
				Namespace: ns,
				Labels: map[string]string{
					"kuso.sislelabs.com/project": desiredName,
				},
			},
			Data: byteData,
		}
		if _, err := h.Kube.Clientset.CoreV1().Secrets(ns).Create(ctx, sec, metav1.CreateOptions{}); err != nil {
			if apierrors.IsAlreadyExists(err) {
				// Update in place. Idempotent re-imports just refresh
				// the values, matching what `kubectl apply` would do.
				existing, gerr := h.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, secName, metav1.GetOptions{})
				if gerr == nil {
					existing.Data = byteData
					if _, uerr := h.Kube.Clientset.CoreV1().Secrets(ns).Update(ctx, existing, metav1.UpdateOptions{}); uerr != nil {
						out.Warnings = append(out.Warnings, fmt.Sprintf("update env secret %s: %v", secName, uerr))
						continue
					}
				}
			} else {
				out.Warnings = append(out.Warnings, fmt.Sprintf("create env secret %s: %v", secName, err))
				continue
			}
		}
		out.Secrets += len(byteData)
	}

	h.Logger.Info("project imported",
		"project", desiredName,
		"services", out.Services,
		"envs", out.Environments,
		"addons", out.Addons,
		"secretKeys", out.Secrets,
		"warnings", len(out.Warnings),
	)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// applyProjectRename rewrites .metadata.name and the spec's .project
// field when the source project name appears in either. Idempotent
// no-op when renameMap is empty.
func applyProjectRename(meta *metav1.ObjectMeta, specProject *string, renameMap map[string]string) {
	if len(renameMap) == 0 {
		return
	}
	if specProject != nil {
		if new, ok := renameMap[*specProject]; ok {
			*specProject = new
		}
	}
	// metadata.name is typically "<project>-<short>"; rewrite the
	// project prefix when it matches.
	for old, new := range renameMap {
		if meta.Name == old {
			meta.Name = new
			break
		}
		if pathHasPrefix(meta.Name, old+"-") {
			meta.Name = new + meta.Name[len(old):]
			break
		}
	}
	// kuso.sislelabs.com/project label too — used by the env-group
	// label selector + the shared-secret cleanup sweeper.
	if meta.Labels != nil {
		if v, ok := meta.Labels["kuso.sislelabs.com/project"]; ok {
			if new, ok := renameMap[v]; ok {
				meta.Labels["kuso.sislelabs.com/project"] = new
			}
		}
	}
}

// pathHasPrefix / pathHasSuffix / pathTrimPrefix / pathTrimSuffix are
// thin wrappers around strings.* — kept local so the import code is
// self-contained and reads top-to-bottom without an import bounce.
func pathHasPrefix(s, prefix string) bool { return len(s) >= len(prefix) && s[:len(prefix)] == prefix }
func pathHasSuffix(s, suffix string) bool { return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix }
func pathTrimPrefix(s, prefix string) string {
	if pathHasPrefix(s, prefix) {
		return s[len(prefix):]
	}
	return s
}
func pathTrimSuffix(s, suffix string) string {
	if pathHasSuffix(s, suffix) {
		return s[:len(s)-len(suffix)]
	}
	return s
}

// stripServerMeta clears fields the apiserver populates that aren't
// useful (and would actively confuse) when re-applied on a different
// cluster: ResourceVersion, UID, ManagedFields, OwnerReferences,
// CreationTimestamp. Labels + annotations are preserved because
// they're part of the spec contract.
func stripServerMeta(m *metav1.ObjectMeta) {
	m.ResourceVersion = ""
	m.UID = ""
	m.SelfLink = ""
	m.Generation = 0
	m.CreationTimestamp = metav1.Time{}
	m.DeletionTimestamp = nil
	m.DeletionGracePeriodSeconds = nil
	m.ManagedFields = nil
	m.OwnerReferences = nil
	m.Finalizers = nil
}

// nsFor resolves the execution namespace for a project, falling back
// to the home namespace when no per-project override exists. Mirrors
// what addons.Service.nsFor does — kept here so the export handler
// doesn't need to reach into another package's private methods.
func (h *ExportHandler) nsFor(ctx context.Context, project string) string {
	if h.NSResolver == nil {
		return h.Namespace
	}
	if ns := h.NSResolver.NamespaceFor(ctx, project); ns != "" {
		return ns
	}
	return h.Namespace
}
