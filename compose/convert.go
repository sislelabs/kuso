package compose

import (
	"sort"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"gopkg.in/yaml.v3"
)

// Doc is the kuso.yaml document the converter emits. It is a local
// mirror of server-go's internal spec.File (which can't be imported
// across modules); the field set and yaml tags match so the marshaled
// output round-trips through spec.Parse on apply.
type Doc struct {
	APIVersion string    `yaml:"apiVersion"`
	Project    string    `yaml:"project"`
	Services   []Service `yaml:"services,omitempty"`
	Addons     []Addon   `yaml:"addons,omitempty"`
}

// Service mirrors spec.ServiceSpec (the subset the converter produces).
type Service struct {
	Name      string            `yaml:"name"`
	Runtime   string            `yaml:"runtime,omitempty"`
	Repo      string            `yaml:"repo,omitempty"`
	Path      string            `yaml:"path,omitempty"`
	Port      int32             `yaml:"port,omitempty"`
	Command   []string          `yaml:"command,omitempty"`
	Domains   []Domain          `yaml:"domains,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
	Scale     *Scale            `yaml:"scale,omitempty"`
	Volumes   []Volume          `yaml:"volumes,omitempty"`
	Image     *Image            `yaml:"image,omitempty"`
	BuildArgs map[string]string `yaml:"buildArgs,omitempty"`
}

type Domain struct {
	Host string `yaml:"host"`
	TLS  bool   `yaml:"tls,omitempty"`
}

type Scale struct {
	Min int `yaml:"min,omitempty"`
	Max int `yaml:"max,omitempty"`
}

type Volume struct {
	Name      string `yaml:"name"`
	MountPath string `yaml:"mountPath"`
	SizeGi    int    `yaml:"sizeGi,omitempty"`
}

type Image struct {
	Repository string `yaml:"repository,omitempty"`
	Tag        string `yaml:"tag,omitempty"`
}

// Addon mirrors spec.AddonSpec (the subset the converter produces).
type Addon struct {
	Name        string `yaml:"name"`
	Kind        string `yaml:"kind"`
	Version     string `yaml:"version,omitempty"`
	StorageSize string `yaml:"storageSize,omitempty"`
}

// defaultVolumeSizeGi is the PVC size assigned to a named volume when
// compose gives no size hint (compose has no size concept).
const defaultVolumeSizeGi = 5

// addonRef is what depends_on env-rewriting needs to know about a
// classified addon: its kuso slug and kind (the kind picks the conn-
// secret URL key, which differs per datastore).
type addonRef struct {
	slug string
	kind string
}

// Convert turns a parsed compose project into a kuso.yaml Doc and a
// Report. projectName is the kuso project slug to use (caller-supplied,
// e.g. the compose file's directory name). The conversion never fails:
// anything that can't be mapped is recorded in the Report as a flag or
// skip rather than dropped or errored.
func Convert(proj *types.Project, projectName string) (*Doc, *Report) {
	rep := &Report{}
	doc := &Doc{APIVersion: "kuso/v1", Project: slugify(projectName)}

	// Deterministic order: compose stores services in a map.
	names := make([]string, 0, len(proj.Services))
	for name := range proj.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	// First pass: classify each compose service as addon or app, and
	// record the addon set so depends_on rewriting can reference it.
	slugFor := dedupeSlugs(names)
	addonByCompose := map[string]addonRef{} // compose service name → addon slug+kind
	for _, name := range names {
		svc := proj.Services[name]
		if kind, _ := classifyDatastore(svc.Image); kind != "" {
			addonByCompose[name] = addonRef{slug: slugFor[name], kind: kind}
		}
	}

	for _, name := range names {
		svc := proj.Services[name]
		slug := slugFor[name]
		if kind, version := classifyDatastore(svc.Image); kind != "" {
			doc.Addons = append(doc.Addons, convertAddon(svc, slug, kind, version, rep))
			continue
		}
		doc.Services = append(doc.Services, convertService(svc, slug, addonByCompose, rep))
	}

	return doc, rep
}

// Marshal renders the Doc as kuso.yaml bytes.
func (d *Doc) Marshal() ([]byte, error) {
	return yaml.Marshal(d)
}

func convertAddon(svc types.ServiceConfig, slug, kind, version string, rep *Report) Addon {
	a := Addon{Name: slug, Kind: kind, Version: version}
	// The managed addon provisions FRESH, EMPTY storage — nothing from
	// the compose side is attached or copied. Flag every trace of
	// existing state (data volumes, credentials, init-script mounts) so
	// the user knows a manual migration stands between them and cutover.
	for _, v := range svc.Volumes {
		switch {
		case v.Type == "volume" && v.Source != "":
			rep.flag(svc.Name, "data volume %q is NOT attached or copied — the managed addon starts with fresh, empty storage; migrate existing data by hand (dump & restore) before switching traffic", v.Source)
		case v.Type == "bind":
			rep.flag(svc.Name, "bind mount %q→%q is NOT imported — init scripts / seed data mounted there do not run against the managed addon", v.Source, v.Target)
		}
	}
	if len(svc.Environment) > 0 {
		rep.flag(svc.Name, "datastore environment (%d vars — users, passwords, database names) is NOT carried over — kuso mints its own credentials in the addon's conn-secret", len(svc.Environment))
	}
	if len(svc.EnvFiles) > 0 {
		rep.flag(svc.Name, "datastore env_file(s) NOT read — any users/passwords/db names in them are NOT carried over; kuso mints its own credentials in the addon's conn-secret")
	}
	verNote := version
	if verNote == "" {
		verNote = "(chart default)"
	}
	rep.addon(svc.Name, "→ addon `%s` (kind=%s, version=%s); project services get its conn vars automatically. The addon starts EMPTY — no data is migrated from the compose deployment", slug, kind, verNote)
	noteUnmapped(svc, rep)
	return a
}

func convertService(svc types.ServiceConfig, slug string, addons map[string]addonRef, rep *Report) Service {
	out := Service{Name: slug}

	// Runtime: build context → dockerfile; else pre-built image → image.
	switch {
	case svc.Build != nil:
		out.Runtime = "dockerfile"
		rep.service(svc.Name, "build context %q → runtime=dockerfile; set `repo:` to the service's git URL before deploying", svc.Build.Context)
		rep.flag(svc.Name, "no repo set — kuso builds from a git repo; fill in `repo:` for service `%s`", slug)
		if p := repoSubPath(svc.Build.Context); p != "" {
			out.Path = p
			rep.service(svc.Name, "build context %q → path: %q (assumes the compose file sits at the repo root — adjust if not)", svc.Build.Context, p)
		}
		if svc.Build.Dockerfile != "" && svc.Build.Dockerfile != "Dockerfile" {
			rep.flag(svc.Name, "build.dockerfile=%q has no kuso field — kuso builds `Dockerfile` at the service path; rename or move it in the repo", svc.Build.Dockerfile)
		}
		if svc.Build.Target != "" {
			rep.flag(svc.Name, "build.target=%q has no kuso field — kuso builds the Dockerfile's final stage", svc.Build.Target)
		}
		out.BuildArgs = convertBuildArgs(svc, rep)
	case svc.Image != "":
		repo, tag := imageParts(svc.Image)
		digest := imageDigest(svc.Image)
		if tag == "" {
			tag = "latest"
		}
		out.Runtime = "image"
		out.Image = &Image{Repository: repo, Tag: tag}
		rep.service(svc.Name, "image %q → runtime=image (%s:%s)", svc.Image, repo, tag)
		// kuso image services are repository:tag only (the env chart
		// renders exactly that), so a digest pin can't be preserved.
		// Flag it loudly — the reproducible artifact just became a
		// mutable tag — rather than silently defaulting.
		if digest != "" {
			rep.flag(svc.Name, "digest pin %s dropped — kuso image services take repository:tag only, so the deploy tracks the MUTABLE %s:%s; push or pick an immutable tag if you need reproducibility", digest, repo, tag)
		}
		// A datastore image kuso doesn't have a managed addon for yet:
		// it deploys fine as a raw image service, but the user loses the
		// managed-addon goodies (conn-secret, backups). Flag so they
		// know it wasn't turned into an addon on purpose.
		if reserved := maybeReservedDatastore(svc.Image); reserved != "" {
			rep.flag(svc.Name, "%q looks like a %s datastore, but kuso has no managed %s addon yet — kept as a plain image service (no conn-secret / backups)", svc.Image, reserved, reserved)
		}
	default:
		out.Runtime = "dockerfile"
		rep.flag(svc.Name, "no image and no build — set `repo:` + runtime for service `%s`", slug)
	}

	// Ports: first published port → service port + a domain entry.
	out.Port, out.Domains = convertPorts(svc, slug, rep)

	// Environment + env_file → env map, with depends_on-driven addon
	// rewrite where a value names a datastore service.
	out.Env = convertEnv(svc, addons, rep)

	// Volumes: named → KusoVolume; bind/anon → flagged.
	out.Volumes = convertVolumes(svc, rep)

	// deploy.replicas → scale.
	if svc.Deploy != nil && svc.Deploy.Replicas != nil && *svc.Deploy.Replicas > 0 {
		n := *svc.Deploy.Replicas
		out.Scale = &Scale{Min: n, Max: n}
		rep.service(svc.Name, "deploy.replicas=%d → scale.min/max=%d", n, n)
	}

	// command override.
	if len(svc.Command) > 0 {
		out.Command = []string(svc.Command)
		rep.service(svc.Name, "command → command override")
	}

	noteUnmapped(svc, rep)
	return out
}

func convertPorts(svc types.ServiceConfig, slug string, rep *Report) (int32, []Domain) {
	if len(svc.Ports) == 0 {
		return 0, nil
	}
	first := svc.Ports[0]
	port := int32(first.Target)
	domains := []Domain{}
	if first.Published != "" || first.Target != 0 {
		rep.service(svc.Name, "ports %q → port=%d + public domain (auto `%s.<project>.<kuso-domain>`)", portString(first), port, slug)
	}
	if len(svc.Ports) > 1 {
		extra := make([]string, 0, len(svc.Ports)-1)
		for _, p := range svc.Ports[1:] {
			extra = append(extra, portString(p))
		}
		rep.skip(svc.Name, "extra ports %s not imported — kuso exposes one port per service", strings.Join(extra, ", "))
	}
	return port, domains
}

func portString(p types.ServicePortConfig) string {
	if p.Published != "" {
		return p.Published + ":" + strconv.FormatUint(uint64(p.Target), 10)
	}
	return strconv.FormatUint(uint64(p.Target), 10)
}

func convertEnv(svc types.ServiceConfig, addons map[string]addonRef, rep *Report) map[string]string {
	noteEnvFiles(svc, rep)
	if len(svc.Environment) == 0 {
		return nil
	}
	env := map[string]string{}
	for k, vp := range svc.Environment {
		val := ""
		if vp != nil {
			val = *vp
		}
		if rewritten, ref, ok := rewriteAddonRef(val, addons); ok {
			env[k] = rewritten
			rep.service(svc.Name, "env %s references addon `%s` → rewritten to %s", k, ref.slug, rewritten)
			continue
		}
		env[k] = val
	}
	return env
}

// noteEnvFiles flags env_file entries as BLOCKING: the parser never
// reads env-file contents (see Parse), so the converted services would
// deploy without whatever configuration or credentials those files
// carry. The CLI refuses --apply while Report.UnresolvedEnvFiles is
// non-empty unless the user explicitly overrides.
func noteEnvFiles(svc types.ServiceConfig, rep *Report) {
	if len(svc.EnvFiles) == 0 {
		return
	}
	names := make([]string, 0, len(svc.EnvFiles))
	for _, e := range svc.EnvFiles {
		names = append(names, e.Path)
		rep.unresolvedEnvFile(e.Path)
	}
	rep.flag(svc.Name, "env_file %s NOT read — its values are missing from the import; copy them into kuso env vars before deploying", strings.Join(names, ", "))
}

// convertBuildArgs maps compose build.args onto kuso's buildArgs.
// Args with no value (host-env pass-through) can't be resolved — the
// import mustn't depend on the importer's shell — so they're flagged
// for the user to fill in by hand.
func convertBuildArgs(svc types.ServiceConfig, rep *Report) map[string]string {
	if svc.Build == nil || len(svc.Build.Args) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range svc.Build.Args {
		if v == nil {
			rep.flag(svc.Name, "build arg %s has no value in compose (host-env pass-through) — set it under `buildArgs:` by hand", k)
			continue
		}
		out[k] = *v
	}
	if len(out) == 0 {
		return nil
	}
	rep.service(svc.Name, "build.args → buildArgs (%d value(s))", len(out))
	return out
}

// rewriteAddonRef rewrites a connection-string-ish env value that
// points at a datastore service hostname into kuso's
// ${{ addon.<URLKEY> }} reference form. The URL key is per kind
// (postgres→DATABASE_URL, redis→REDIS_URL, …) so the resolved
// secretKeyRef actually exists in the addon's conn-secret. It only
// fires when the value's host segment exactly matches a known addon's
// compose service name — a conservative match so unrelated values pass
// through untouched.
func rewriteAddonRef(val string, addons map[string]addonRef) (string, addonRef, bool) {
	for composeName, ref := range addons {
		key := addonURLKey(ref.kind)
		if key == "" {
			continue
		}
		// Match "scheme://…@<composeName>[:port]/…" or a bare host.
		if strings.Contains(val, "@"+composeName+":") ||
			strings.Contains(val, "@"+composeName+"/") ||
			strings.Contains(val, "//"+composeName+":") ||
			strings.Contains(val, "//"+composeName+"/") ||
			val == composeName {
			return "${{ " + ref.slug + "." + key + " }}", ref, true
		}
	}
	return "", addonRef{}, false
}

func convertVolumes(svc types.ServiceConfig, rep *Report) []Volume {
	var out []Volume
	for _, v := range svc.Volumes {
		switch v.Type {
		case "volume":
			name := slugify(v.Source)
			if v.Source == "" {
				rep.skip(svc.Name, "anonymous volume at %q not imported — name it in compose to get a kuso volume", v.Target)
				continue
			}
			out = append(out, Volume{Name: name, MountPath: v.Target, SizeGi: defaultVolumeSizeGi})
			rep.service(svc.Name, "volume %q → kuso volume `%s` at %s (%dGi default)", v.Source, name, v.Target, defaultVolumeSizeGi)
		case "bind":
			rep.skip(svc.Name, "bind mount %q→%q not imported — host paths don't exist on kuso nodes; use a named volume", v.Source, v.Target)
		case "tmpfs":
			rep.skip(svc.Name, "tmpfs mount at %q not imported", v.Target)
		default:
			rep.skip(svc.Name, "volume at %q (type=%s) not imported", v.Target, v.Type)
		}
	}
	return out
}

// noteUnmapped flags compose keys that kuso has no field for, so the
// user knows exactly what they must handle by hand.
func noteUnmapped(svc types.ServiceConfig, rep *Report) {
	if svc.HealthCheck != nil {
		rep.skip(svc.Name, "healthcheck not imported — kuso auto-detects readiness/liveness by runtime")
	}
	if svc.Restart != "" {
		rep.skip(svc.Name, "restart=%q not imported — kuso always restarts crashed pods", svc.Restart)
	}
	// Only flag networks the user explicitly set. compose normalizes a
	// lone implicit `default` network onto every service, which the
	// user never wrote — flagging it would be pure noise.
	if explicitNetworks(svc.Networks) {
		rep.skip(svc.Name, "networks not imported — kuso services reach each other via ${{ svc.URL }}")
	}
	if len(svc.Profiles) > 0 {
		rep.skip(svc.Name, "profiles not imported")
	}
	if svc.Privileged {
		rep.skip(svc.Name, "privileged not imported — not supported on kuso")
	}
	if len(svc.CapAdd) > 0 {
		rep.skip(svc.Name, "cap_add %s not imported", strings.Join(svc.CapAdd, ", "))
	}
	if len(svc.Entrypoint) > 0 {
		rep.skip(svc.Name, "entrypoint not imported — set it in your Dockerfile or use command")
	}
	if svc.WorkingDir != "" {
		rep.skip(svc.Name, "working_dir=%q not imported — set WORKDIR in the Dockerfile", svc.WorkingDir)
	}
	if svc.User != "" {
		rep.skip(svc.Name, "user=%q not imported — set USER in the Dockerfile", svc.User)
	}
	if len(svc.DependsOn) > 0 {
		rep.skip(svc.Name, "depends_on not imported — kuso has no start ordering (incl. condition: service_healthy); services must retry their dependencies")
	}
	if len(svc.Secrets) > 0 {
		rep.flag(svc.Name, "secrets not imported — recreate each as a kuso env var (the secret file contents are NOT read)")
	}
	if len(svc.Configs) > 0 {
		rep.flag(svc.Name, "configs not imported — bake the files into the image or recreate the values as env vars")
	}
	if len(svc.Labels) > 0 {
		rep.skip(svc.Name, "labels not imported")
	}
	if svc.Deploy != nil && (svc.Deploy.Resources.Limits != nil || svc.Deploy.Resources.Reservations != nil) {
		rep.skip(svc.Name, "deploy.resources not imported — set the pod size in kuso after import")
	}
	if svc.MemLimit > 0 || svc.CPUS > 0 {
		rep.skip(svc.Name, "mem_limit/cpus not imported — set the pod size in kuso after import")
	}
	if len(svc.ExtraHosts) > 0 {
		rep.skip(svc.Name, "extra_hosts not imported")
	}
	if len(svc.Sysctls) > 0 {
		rep.skip(svc.Name, "sysctls not imported")
	}
	if len(svc.Ulimits) > 0 {
		rep.skip(svc.Name, "ulimits not imported")
	}
	if svc.Logging != nil {
		rep.skip(svc.Name, "logging not imported — kuso captures stdout/stderr")
	}
	if svc.StopGracePeriod != nil {
		rep.skip(svc.Name, "stop_grace_period not imported")
	}
}

// explicitNetworks reports whether the user attached this service to a
// network beyond compose's implicit `default`. A map containing only
// "default" (or nothing) means the user wrote no `networks:` key.
func explicitNetworks(nets map[string]*types.ServiceNetworkConfig) bool {
	for name := range nets {
		if name != "default" {
			return true
		}
	}
	return false
}
