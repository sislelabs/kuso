import { ApiError } from "@/lib/api-client";

// Why the add-service flow couldn't read a repo to detect its runtime.
// Each kind maps to one explanation and the actions that can fix it.
export type RepoReadFailureKind =
  | "network"
  | "app-not-configured"
  | "app-not-installed"
  | "app-no-access"
  | "app-forbidden"
  | "branch-unreadable"
  | "not-found"
  | "path-not-found"
  | "token-rejected"
  | "rate-limit"
  | "host-unreachable"
  | "invalid-url"
  | "unsupported-host"
  | "empty-repo"
  | "server";

export type RepoReadAction = "grant-access" | "setup-github" | "use-token";

export interface RepoReadFailure {
  kind: RepoReadFailureKind;
  title: string;
  detail: string;
  // HTTP status of the failing kuso API call, when there was one.
  status?: number;
  action?: RepoReadAction;
  // False when retrying can't change the answer (unsupported host).
  retryable: boolean;
}

export interface RepoReadFailureInput {
  // "app": read through the GitHub App installation (detect-runtime).
  // "url": read by URL with no App (repos/inspect).
  via: "app" | "url";
  repo: string;
  branch: string;
  // Root directory inside the repo ("" for the root).
  path?: string;
  hasToken: boolean;
  // What the detect / inspect call threw.
  error?: unknown;
  // repos/inspect answered 200 but with no runtime; runtimeNote says why.
  note?: string | null;
  // POST /api/github/check-repo's answer, asked after an App-path failure
  // because detect-runtime reports every GitHub error as a bare 500.
  access?: { ok: boolean; error?: string } | null;
}

const RATE_LIMIT = /rate limit/i;

function isNetworkError(e: unknown): boolean {
  if (e instanceof ApiError) return false;
  // fetch rejects with a TypeError whose text differs per browser:
  // "Failed to fetch" (Chromium), "Load failed" (Safari),
  // "NetworkError when attempting to fetch resource." (Firefox).
  if (e instanceof TypeError) return true;
  return e instanceof Error && /failed to fetch|load failed|networkerror/i.test(e.message);
}

function serverMessage(e: unknown): string {
  if (!(e instanceof Error)) return "";
  const msg = e.message.trim();
  return /^internal( server error)?$/i.test(msg) ? "" : msg;
}

function upperFirst(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

function rateLimit(status: number | undefined, hasToken: boolean): RepoReadFailure {
  return {
    kind: "rate-limit",
    title: "GitHub's API rate limit was hit",
    detail: hasToken
      ? "GitHub is throttling requests for this token. Wait a few minutes and retry."
      : "Anonymous GitHub API calls share 60 requests an hour per IP. Wait and retry, or add an access token.",
    status,
    action: hasToken ? undefined : "use-token",
    retryable: true,
  };
}

function notFound(repo: string, hasToken: boolean, status?: number): RepoReadFailure {
  return hasToken
    ? {
        kind: "token-rejected",
        title: "The access token can't read this repository",
        detail: `GitHub answered as if ${repo} doesn't exist for this token. Check the URL, and that the token has read access to the repo's contents.`,
        status,
        action: "use-token",
        retryable: true,
      }
    : {
        kind: "not-found",
        title: "Repository not found",
        detail: `Nothing public lives at ${repo}. Check the URL for a typo. If the repo is private, add an access token.`,
        status,
        action: "use-token",
        retryable: true,
      };
}

function fromNote(note: string, input: RepoReadFailureInput): RepoReadFailure {
  if (/only works for github\.com/i.test(note)) {
    return {
      kind: "unsupported-host",
      title: "Runtime detection only works for github.com repos",
      detail: "kuso can still clone and build this repo. Pick the runtime yourself.",
      retryable: false,
    };
  }
  if (RATE_LIMIT.test(note)) return rateLimit(undefined, input.hasToken);
  if (/no branches/i.test(note)) {
    return {
      kind: "empty-repo",
      title: "The repository has no branches yet",
      detail: "Push a first commit, then retry.",
      retryable: true,
    };
  }
  // A note means the repo's refs were readable, so a 404 here is about
  // the folder or branch, unless a token is in play (it may lack Contents).
  if (/is not a directory/i.test(note) || (/not found or not accessible/i.test(note) && !input.hasToken)) {
    const dir = (input.path ?? "").trim().replace(/^\/+|\/+$/g, "");
    return {
      kind: "path-not-found",
      title: dir ? `No folder ${dir}/ on branch ${input.branch}` : `Couldn't list branch ${input.branch}`,
      detail: "The repository is reachable, but GitHub has nothing at that location. Check the root directory and branch.",
      retryable: true,
    };
  }
  if (/not found or not accessible/i.test(note)) return notFound(input.repo, input.hasToken);
  if (/reach github/i.test(note)) {
    return {
      kind: "host-unreachable",
      title: "kuso couldn't reach GitHub",
      detail: "The kuso server's request to api.github.com failed. Retry in a moment.",
      retryable: true,
    };
  }
  return {
    kind: "server",
    title: "Runtime detection failed",
    detail: upperFirst(note.trim()),
    retryable: true,
  };
}

function fromAccess(access: { ok: boolean; error?: string }, input: RepoReadFailureInput, status?: number): RepoReadFailure | null {
  const owner = input.repo.split("/")[0] || input.repo;
  if (access.ok) {
    // The App can see the repo, so the tree read itself failed: almost
    // always a branch that no longer exists.
    return {
      kind: "branch-unreadable",
      title: `kuso can see ${input.repo} but couldn't read branch ${input.branch}`,
      detail: "The branch may have been renamed or deleted, or GitHub returned an error. Pick the repo again to refresh its branch, or retry.",
      status,
      retryable: true,
    };
  }
  const err = access.error ?? "";
  if (/no github app installation/i.test(err)) {
    return {
      kind: "app-not-installed",
      title: `kuso's GitHub App isn't installed on ${owner}`,
      detail: `Install the App on ${owner} and give it access to ${input.repo}, then retry.`,
      status,
      action: "grant-access",
      retryable: true,
    };
  }
  if (/not found or app has no access/i.test(err)) {
    return {
      kind: "app-no-access",
      title: `kuso's GitHub App can't see ${input.repo}`,
      detail: `The App's installation on ${owner} doesn't include this repo, or it was renamed or deleted. Add it to the App's repository access, then retry.`,
      status,
      action: "grant-access",
      retryable: true,
    };
  }
  if (RATE_LIMIT.test(err)) return rateLimit(status, true);
  if (/forbidden/i.test(err)) {
    return {
      kind: "app-forbidden",
      title: `GitHub refused kuso's App access to ${input.repo}`,
      detail: "The installation may be suspended, or it lacks the Contents permission. Review it in the App's installation settings.",
      status,
      action: "grant-access",
      retryable: true,
    };
  }
  return null;
}

// classifyRepoReadFailure turns whatever the detection calls produced
// into one user-facing reason. Pure, so every branch is unit-tested.
export function classifyRepoReadFailure(input: RepoReadFailureInput): RepoReadFailure {
  const { error } = input;
  if (error !== undefined && isNetworkError(error)) {
    return {
      kind: "network",
      title: "Couldn't reach the kuso server",
      detail: "The request never got an answer. Check your connection or VPN, then retry.",
      retryable: true,
    };
  }
  if (error === undefined && input.note) return fromNote(input.note, input);

  const status = error instanceof ApiError ? error.status : undefined;
  const msg = serverMessage(error);

  if (status === 503 && /github not configured/i.test(msg)) {
    return {
      kind: "app-not-configured",
      title: "GitHub isn't connected to kuso",
      detail: "An admin needs to set up the GitHub App before kuso can read repos through it.",
      status,
      action: "setup-github",
      retryable: true,
    };
  }
  if (status === 429 || RATE_LIMIT.test(msg)) return rateLimit(status, input.hasToken);

  if (input.via === "app" && input.access) {
    const fromCheck = fromAccess(input.access, input, status);
    if (fromCheck) return fromCheck;
  }

  if (input.via === "url") {
    if (status === 404) return notFound(input.repo, input.hasToken, status);
    if (status === 502) {
      return {
        kind: "host-unreachable",
        title: "kuso couldn't reach the repository's git host",
        detail: "The host didn't answer the kuso server. Check the URL, or retry if the host is having trouble.",
        status,
        retryable: true,
      };
    }
    if (status === 400) {
      return {
        kind: "invalid-url",
        title: "kuso can't use this repository URL",
        detail: msg ? upperFirst(msg) : "Use the https:// clone URL of the repository.",
        status,
        retryable: false,
      };
    }
  }

  return {
    kind: "server",
    title: status ? `Runtime detection failed (HTTP ${status})` : "Runtime detection failed",
    detail: msg ? upperFirst(msg) : "kuso-server hit an internal error and didn't say why. Its logs have the GitHub error.",
    status,
    retryable: true,
  };
}
