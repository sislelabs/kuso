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
	Name    string            `yaml:"name"`
	Runtime string            `yaml:"runtime,omitempty"`
	Repo    string            `yaml:"repo,omitempty"`
	Port    int32             `yaml:"port,omitempty"`
	Command []string          `yaml:"command,omitempty"`
	Domains []Domain          `yaml:"domains,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	Scale   *Scale            `yaml:"scale,omitempty"`
	Volumes []Volume          `yaml:"volumes,omitempty"`
	Image   *Image            `yaml:"image,omitempty"`
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
	addonByCompose := map[string]string{} // compose service name → kuso addon slug
	for _, name := range names {
		svc := proj.Services[name]
		if kind, _ := classifyDatastore(svc.Image); kind != "" {
			addonByCompose[name] = slugFor[name]
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
	// A named volume on the datastore becomes the addon's storage; we
	// can't read a size from compose, so leave it at the chart default
	// and just note the data path.
	for _, v := range svc.Volumes {
		if v.Type == "volume" && v.Source != "" {
			rep.addon(svc.Name, "data volume %q → addon storage (chart default size)", v.Target)
			break
		}
	}
	verNote := version
	if verNote == "" {
		verNote = "(chart default)"
	}
	rep.addon(svc.Name, "→ addon `%s` (kind=%s, version=%s); project services get its conn vars automatically", slug, kind, verNote)
	noteUnmapped(svc, rep)
	return a
}

func convertService(svc types.ServiceConfig, slug string, addons map[string]string, rep *Report) Service {
	out := Service{Name: slug}

	// Runtime: build context → dockerfile; else pre-built image → image.
	switch {
	case svc.Build != nil:
		out.Runtime = "dockerfile"
		rep.service(svc.Name, "build context %q → runtime=dockerfile; set `repo:` to the service's git URL before deploying", svc.Build.Context)
		rep.flag(svc.Name, "no repo set — kuso builds from a git repo; fill in `repo:` for service `%s`", slug)
	case svc.Image != "":
		repo, tag := imageParts(svc.Image)
		if tag == "" {
			tag = "latest"
		}
		out.Runtime = "image"
		out.Image = &Image{Repository: repo, Tag: tag}
		rep.service(svc.Name, "image %q → runtime=image (%s:%s)", svc.Image, repo, tag)
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

func convertEnv(svc types.ServiceConfig, addons map[string]string, rep *Report) map[string]string {
	if len(svc.Environment) == 0 {
		if len(svc.EnvFiles) > 0 {
			names := make([]string, 0, len(svc.EnvFiles))
			for _, e := range svc.EnvFiles {
				names = append(names, e.Path)
			}
			rep.skip(svc.Name, "env_file %s not inlined — values weren't read; copy them into kuso env vars", strings.Join(names, ", "))
		}
		return nil
	}
	env := map[string]string{}
	// Build a lookup of addon service-name → slug for value rewriting.
	for k, vp := range svc.Environment {
		val := ""
		if vp != nil {
			val = *vp
		}
		if rewritten, addon, ok := rewriteAddonRef(val, addons); ok {
			env[k] = rewritten
			rep.service(svc.Name, "env %s references addon `%s` → rewritten to ${{ %s.URL }} form", k, addon, addon)
			continue
		}
		env[k] = val
	}
	if len(svc.EnvFiles) > 0 {
		names := make([]string, 0, len(svc.EnvFiles))
		for _, e := range svc.EnvFiles {
			names = append(names, e.Path)
		}
		rep.skip(svc.Name, "env_file %s not inlined — copy any needed values into kuso env vars", strings.Join(names, ", "))
	}
	return env
}

// rewriteAddonRef rewrites a connection-string-ish env value that
// points at a datastore service hostname into kuso's ${{ addon.URL }}
// reference form. It only fires when the value's host segment exactly
// matches a known addon's compose service name — a conservative match
// so unrelated values pass through untouched.
func rewriteAddonRef(val string, addons map[string]string) (string, string, bool) {
	for composeName, slug := range addons {
		// Match "scheme://…@<composeName>[:port]/…" or a bare host.
		if strings.Contains(val, "@"+composeName+":") ||
			strings.Contains(val, "@"+composeName+"/") ||
			strings.Contains(val, "//"+composeName+":") ||
			strings.Contains(val, "//"+composeName+"/") ||
			val == composeName {
			return "${{ " + slug + ".URL }}", slug, true
		}
	}
	return "", "", false
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
