// Resource classifier. Each Coolify resource (Application, Service,
// Database) is mapped to a Verdict that decides whether the
// migration tool will create a kuso CR for it, skip it (with a
// reason), or flag it for human review.

package coolify

import "strings"

type Kind string

const (
	// KindCoolifyApp = nixpacks/dockerfile/static. Auto-convertible
	// to a KusoService.
	KindCoolifyApp Kind = "coolify-app"
	// KindComposeSkip = build_pack=dockercompose. User asked to skip
	// these entirely. We still surface them in the report so the
	// operator knows what wasn't migrated.
	KindComposeSkip Kind = "compose-skip"
	// KindCoolifyService = Coolify "service stack" (one-click
	// postgres+pgadmin etc.). Always docker-compose under the hood,
	// so also skip.
	KindCoolifyService Kind = "service-stack-skip"
	// KindDatabase = standalone DB. Maps to KusoAddon.
	KindDatabase Kind = "database"
)

// Verdict is the result of classifying one resource.
type Verdict struct {
	Kind   Kind
	Action string // "migrate" | "skip" | "flag"
	Reason string // human-readable for the report
}

func ClassifyApplication(a Application) Verdict {
	switch strings.ToLower(a.BuildPack) {
	case "nixpacks", "dockerfile", "static":
		return Verdict{Kind: KindCoolifyApp, Action: "migrate",
			Reason: "git-backed " + a.BuildPack + " app — direct port to KusoService"}
	case "dockercompose":
		return Verdict{Kind: KindComposeSkip, Action: "skip",
			Reason: "docker-compose stacks are skipped per migration policy — kuso doesn't run compose; rewrite as N services or keep on Coolify"}
	default:
		return Verdict{Kind: KindCoolifyApp, Action: "flag",
			Reason: "unknown build_pack=" + a.BuildPack + " — review manually"}
	}
}

// ClassifyService — every Coolify "service stack" is skipped because
// they're docker-compose under the hood.
func ClassifyService(_ Service) Verdict {
	return Verdict{Kind: KindCoolifyService, Action: "skip",
		Reason: "Coolify service stack (docker-compose) — skip per migration policy"}
}

// ClassifyDatabase maps Coolify's "standalone-<engine>" labels to
// kuso addon kinds. Returns flag (not migrate) when the engine isn't
// supported by kuso addons today.
func ClassifyDatabase(d Database) Verdict {
	short := strings.TrimPrefix(strings.ToLower(d.DatabaseType), "standalone-")
	switch short {
	case "postgresql", "postgres":
		return Verdict{Kind: KindDatabase, Action: "migrate", Reason: "postgres → KusoAddon kind=postgres"}
	case "redis":
		return Verdict{Kind: KindDatabase, Action: "migrate", Reason: "redis → KusoAddon kind=redis"}
	case "mongodb":
		return Verdict{Kind: KindDatabase, Action: "flag", Reason: "mongodb addon is skeleton-only on kuso v0.6 — review before migrating"}
	case "mysql", "mariadb":
		return Verdict{Kind: KindDatabase, Action: "flag", Reason: short + " addon is skeleton-only on kuso v0.6 — review before migrating"}
	case "clickhouse", "dragonfly", "keydb":
		return Verdict{Kind: KindDatabase, Action: "flag", Reason: short + " is unsupported on kuso v0.6 — manual migration"}
	default:
		return Verdict{Kind: KindDatabase, Action: "flag", Reason: "unknown database_type=" + d.DatabaseType + " — review manually"}
	}
}

// AddonKindFromCoolify maps a Coolify standalone-* type onto a kuso
// addon kind, returning empty when no mapping exists.
func AddonKindFromCoolify(coolifyType string) string {
	short := strings.TrimPrefix(strings.ToLower(coolifyType), "standalone-")
	switch short {
	case "postgres", "postgresql":
		return "postgres"
	case "redis":
		return "redis"
	case "mongodb":
		return "mongodb"
	case "mysql":
		return "mysql"
	case "mariadb":
		return "mysql" // closest kuso equivalent; addon helm doesn't distinguish
	}
	return ""
}
