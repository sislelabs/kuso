package compose

import (
	"reflect"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
)

// TestServiceConfigFieldCoverage is a drift guard. compose-go's
// types.ServiceConfig grows and renames fields between releases; the
// converter's handling of them is a hand-maintained allow-list
// (convertService + convertAddon + noteUnmapped), which silently rots
// when a dependency bump adds a field nobody wired up — the exact class
// of bug that let :ro, platform, cap_drop, etc. drop without a peep.
//
// This test enumerates EVERY field of types.ServiceConfig by reflection
// and asserts each is either:
//
//   - consumed  — read by the converter and mapped onto a kuso field, OR
//   - reported  — deliberately surfaced in the Report (flag/skip) so the
//     user knows it wasn't imported, OR
//   - ignored   — an explicit, reviewed decision that this field is safe
//     to drop with no report (e.g. pure Docker-daemon knobs that have no
//     meaning on kuso and no security/data consequence).
//
// A field that is in NONE of the sets fails the test: on the next
// compose-go bump the new field forces a human to classify it rather
// than letting it drift in silently.
func TestServiceConfigFieldCoverage(t *testing.T) {
	// consumed OR reported: the converter looks at these (either mapping
	// them to a kuso field, or emitting a report note that they were not
	// imported). Grep convert.go for each to see where.
	handled := map[string]string{
		"Name":            "service/addon name",
		"Profiles":        "reported (skip) in noteUnmapped",
		"Build":           "consumed → runtime=dockerfile + buildArgs",
		"CapAdd":          "reported (skip) in noteUnmapped",
		"CapDrop":         "reported (flag) in noteUnmapped",
		"Command":         "consumed → command override",
		"Configs":         "reported (flag) in noteUnmapped",
		"ContainerName":   "reported (skip) in noteUnmapped",
		"DependsOn":       "consumed (addon env rewrite) + reported (skip)",
		"Deploy":          "consumed → scale; resources reported (skip)",
		"Devices":         "reported (skip) in noteUnmapped",
		"DNS":             "reported (skip) in noteUnmapped",
		"Dockerfile":      "consumed via Build.Dockerfile path (see convertService)",
		"Entrypoint":      "reported (skip) in noteUnmapped",
		"Environment":     "consumed → env map",
		"EnvFiles":        "reported (flag + UnresolvedEnvFiles)",
		"Expose":          "reported (skip) in noteUnmapped",
		"ExtraHosts":      "reported (skip) in noteUnmapped",
		"HealthCheck":     "reported (skip) in noteUnmapped",
		"Image":           "consumed → runtime=image / addon classification",
		"Init":            "reported (skip) in noteUnmapped",
		"Labels":          "reported (skip) in noteUnmapped",
		"Logging":         "reported (skip) in noteUnmapped",
		"MemLimit":        "reported (skip) in noteUnmapped",
		"CPUS":            "reported (skip) in noteUnmapped",
		"NetworkMode":     "reported (skip) in noteUnmapped",
		"Networks":        "reported (skip, explicit only) in noteUnmapped",
		"Pid":             "reported (skip) in noteUnmapped",
		"Platform":        "reported (flag — cross-arch crashloop) in noteUnmapped",
		"Ports":           "consumed → port + domain",
		"Privileged":      "reported (skip) in noteUnmapped",
		"ReadOnly":        "reported (flag — dropped guardrail) in noteUnmapped",
		"Restart":         "reported (skip) in noteUnmapped",
		"Secrets":         "reported (flag) in noteUnmapped",
		"SecurityOpt":     "reported (flag) in noteUnmapped",
		"StopGracePeriod": "reported (skip) in noteUnmapped",
		"Sysctls":         "reported (skip) in noteUnmapped",
		"Tmpfs":           "reported (skip) in noteUnmapped",
		"Ulimits":         "reported (skip) in noteUnmapped",
		"User":            "reported (skip) in noteUnmapped",
		"Volumes":         "consumed → kuso volumes; ro/bind/tmpfs reported",
		"WorkingDir":      "reported (skip) in noteUnmapped",
		"PostStart":       "reported (skip) in noteUnmapped",
		"PreStop":         "reported (skip) in noteUnmapped",
	}

	// ignored: deliberately dropped with NO report. Each entry is a
	// reviewed decision — these are Docker-daemon / local-runtime knobs
	// with no analogue and no security-or-data consequence on a
	// kuso/kubernetes deployment. Adding a field here is an explicit
	// "this is safe to silently drop" sign-off. Prefer moving a field to
	// `handled` (with a report note) if in any doubt.
	ignored := map[string]string{
		"Annotations":       "compose-internal annotations, no kuso field",
		"Attach":            "compose CLI log-attach behavior, N/A",
		"Develop":           "compose watch/develop, dev-loop only",
		"BlkioConfig":       "host block-io tuning, daemon-only",
		"CgroupParent":      "host cgroup placement, daemon-only",
		"Cgroup":            "cgroup namespace mode, daemon-only",
		"CPUCount":          "windows cpu-count knob, daemon-only",
		"CPUPercent":        "windows cpu-percent knob, daemon-only",
		"CPUPeriod":         "cfs period, pod-size covers this",
		"CPUQuota":          "cfs quota, pod-size covers this",
		"CPURTPeriod":       "realtime cpu period, daemon-only",
		"CPURTRuntime":      "realtime cpu runtime, daemon-only",
		"CPUSet":            "cpu pinning, not exposed on kuso",
		"CPUShares":         "cpu shares, pod-size covers this",
		"CredentialSpec":    "windows gMSA credential spec, N/A",
		"DeviceCgroupRules": "device cgroup rules, daemon-only",
		"DNSOpts":           "resolver options, cluster DNS is fixed",
		"DNSSearch":         "resolver search, cluster DNS is fixed",
		"DomainName":        "container domainname, N/A on kuso",
		"Extends":           "resolved by the compose parser before Convert",
		"ExternalLinks":     "legacy links to non-compose containers, N/A",
		"GroupAdd":          "supplementary GIDs, set USER/GID in Dockerfile",
		"Gpus":              "gpu reservations, not exposed on kuso",
		"Hostname":          "container hostname, set by kubernetes",
		"Ipc":               "ipc namespace mode, daemon-only",
		"Isolation":         "windows isolation mode, N/A",
		"LabelFiles":        "label sources — folded into Labels by parser",
		"CustomLabels":      "compose-internal (yaml:\"-\"), not user config",
		"Links":             "legacy container links, use ${{ svc.URL }}",
		"LogDriver":         "legacy log driver — Logging covers reporting",
		"LogOpt":            "legacy log opts — Logging covers reporting",
		"MemReservation":    "soft memory reservation, pod-size covers this",
		"MemSwapLimit":      "swap limit, no swap on kuso nodes",
		"MemSwappiness":     "swappiness, no swap on kuso nodes",
		"MacAddress":        "container MAC, assigned by the CNI",
		"Net":               "deprecated alias of network_mode",
		"OomKillDisable":    "oom-kill toggle, daemon-only",
		"OomScoreAdj":       "oom score, daemon-only",
		"PidsLimit":         "pids cgroup limit, not exposed on kuso",
		"PullPolicy":        "image pull policy, kuso sets its own",
		"Runtime":           "container runtime (runc/…), fixed on kuso",
		"Scale":             "deprecated top-level scale — Deploy.Replicas used",
		"ShmSize":           "/dev/shm size, not exposed on kuso",
		"StdinOpen":         "interactive stdin, N/A for a deployed service",
		"StopSignal":        "custom stop signal, kuso uses SIGTERM",
		"StorageOpt":        "storage driver opts, daemon-only",
		"Tty":               "allocate a TTY, N/A for a deployed service",
		"UserNSMode":        "userns mode, daemon-only",
		"Uts":               "uts namespace mode, daemon-only",
		"VolumeDriver":      "default volume driver, kuso uses its StorageClass",
		"VolumesFrom":       "share volumes from another container, N/A",
		"Extensions":        "x-* extension fields, not standard config",
	}

	tp := reflect.TypeOf(types.ServiceConfig{})
	for i := 0; i < tp.NumField(); i++ {
		name := tp.Field(i).Name
		_, isHandled := handled[name]
		_, isIgnored := ignored[name]
		if isHandled && isIgnored {
			t.Errorf("field %q is in BOTH handled and ignored — pick one", name)
			continue
		}
		if !isHandled && !isIgnored {
			t.Errorf("types.ServiceConfig.%s is not classified by the converter — "+
				"a compose-go bump added/renamed a field. Wire it into convertService/"+
				"noteUnmapped (and add it to `handled` with a report note), or if it is "+
				"genuinely safe to drop silently, add it to `ignored` with a justification.", name)
		}
	}

	// Guard the guard: every classified name must still exist on the
	// struct, so a rename doesn't leave a dangling entry masking a new
	// unclassified field.
	fieldSet := map[string]bool{}
	for i := 0; i < tp.NumField(); i++ {
		fieldSet[tp.Field(i).Name] = true
	}
	for name := range handled {
		if !fieldSet[name] {
			t.Errorf("`handled` names %q which no longer exists on types.ServiceConfig — stale entry", name)
		}
	}
	for name := range ignored {
		if !fieldSet[name] {
			t.Errorf("`ignored` names %q which no longer exists on types.ServiceConfig — stale entry", name)
		}
	}
}
