import { slugifyServiceName } from "./slug";
import type { SizingBody } from "./createSizing";

// Body of POST /api/projects/{p}/services (projects.CreateServiceRequest).
// Only the fields the web create flows send.
export interface CreateServiceBody {
  name: string;
  displayName: string;
  runtime: string;
  repo?: { url: string; defaultBranch?: string; path?: string; token?: string };
  image?: { repository: string; tag: string; pullSecret?: string };
  dockerfile?: string;
  command?: string[];
  fromService?: string;
  port?: number;
  github?: { installationId: number };
  resources?: SizingBody["resources"];
  scale?: SizingBody["scale"];
  // Request-only: create without starting the first build.
  skipFirstBuild?: boolean;
}

export interface CreateServiceRepoInput {
  url: string;
  branch: string;
  path?: string;
  // Clone token for a pasted private repo; never set for App picks.
  token?: string;
  // 0 when the repo was pasted by URL rather than picked through the App.
  installationId: number;
}

export type CreateServiceInput =
  | {
      source: "image";
      name: string;
      image: string;
      tag: string;
      pullSecret?: string;
      port?: string;
      sizing?: SizingBody;
    }
  | {
      source: "repo";
      name: string;
      repo: CreateServiceRepoInput;
      // The project's spec.defaultRepo, if any.
      projectRepo?: { url?: string; defaultBranch?: string } | null;
      kind: "web" | "worker";
      runtime: string;
      dockerfile?: string;
      command?: string;
      fromService?: string;
      port?: string;
      sizing?: SizingBody;
    };

const normRepo = (u: string) => u.trim().replace(/\/+$/, "").replace(/\.git$/, "").toLowerCase();

// followsProjectRepo: a service on the project's own repo and branch
// leaves its branch unset so it tracks later default-branch changes
// (as CLI-created services do). Any other branch pins the service.
export function followsProjectRepo(
  projectRepo: { url?: string; defaultBranch?: string } | null | undefined,
  url: string,
  branch: string,
): boolean {
  if (!projectRepo?.url) return false;
  return normRepo(projectRepo.url) === normRepo(url) && (projectRepo.defaultBranch || "main") === branch;
}

function parsePort(p: string | undefined): number | undefined {
  const n = p?.trim() ? Number(p) : NaN;
  return Number.isInteger(n) && n > 0 && n <= 65535 ? n : undefined;
}

// buildCreateServiceBody shapes the add-service / new-project forms into
// the create request. Field validation happens in the forms; this only
// maps, trims and drops what doesn't apply to the chosen runtime.
export function buildCreateServiceBody(input: CreateServiceInput): CreateServiceBody {
  const displayName = input.name.trim();
  const base = { name: slugifyServiceName(displayName), displayName };
  const port = parsePort(input.port);
  const sizing = input.sizing ?? {};

  if (input.source === "image") {
    const pullSecret = input.pullSecret?.trim();
    return {
      ...base,
      runtime: "image",
      image: {
        repository: input.image.trim(),
        tag: input.tag.trim() || "latest",
        ...(pullSecret ? { pullSecret } : {}),
      },
      ...(port ? { port } : {}),
      ...sizing,
    };
  }

  const runtime = input.kind === "worker" ? "worker" : input.runtime;
  const path = input.repo.path?.trim();
  const token = input.repo.token?.trim();
  const command = input.command?.trim().split(/\s+/).filter(Boolean) ?? [];
  const dockerfile = input.dockerfile?.trim();
  return {
    ...base,
    repo: {
      url: input.repo.url.trim(),
      ...(followsProjectRepo(input.projectRepo, input.repo.url, input.repo.branch)
        ? {}
        : { defaultBranch: input.repo.branch }),
      ...(path ? { path } : {}),
      ...(token ? { token } : {}),
    },
    runtime,
    ...(runtime === "dockerfile" && dockerfile ? { dockerfile } : {}),
    // A worker has no repo build of its own: it reuses fromService's image.
    ...(runtime === "worker" && command.length ? { command } : {}),
    ...(runtime === "worker" ? { fromService: input.fromService ?? "" } : {}),
    ...(port && runtime !== "worker" ? { port } : {}),
    ...(input.repo.installationId > 0 ? { github: { installationId: input.repo.installationId } } : {}),
    ...sizing,
  };
}
