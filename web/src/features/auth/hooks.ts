"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { ApiError, clearJwt, setJwt } from "@/lib/api-client";
import { getAuthMethods, getProfile, getSession, login as loginApi } from "./api";
import type { LoginInput } from "./schemas";

export const sessionQueryKey = ["auth", "session"] as const;

export interface SessionShape {
  user: {
    id: string;
    name: string;
    email: string;
    image: string | null;
    role: string;
  };
  session: {
    permissions: string[];
    userGroups: string[];
    instanceRole: "admin" | "editor" | "viewer" | "";
    adminAll: boolean;
    projectRoles: Record<string, "admin" | "editor" | "viewer">;
  };
}

export function useSession() {
  return useQuery<SessionShape | null>({
    queryKey: sessionQueryKey,
    queryFn: async () => {
      try {
        const [s, p] = await Promise.all([getSession(), getProfile()]);
        if (!s.isAuthenticated) return null;
        const fullName = [p.firstName, p.lastName].filter(Boolean).join(" ");
        return {
          user: {
            id: p.id,
            name: fullName || p.username,
            email: p.email,
            image: p.image ?? null,
            role: p.role,
          },
          session: {
            permissions: p.permissions ?? [],
            userGroups: p.userGroups ?? [],
            instanceRole: p.instanceRole ?? "",
            adminAll: p.adminAll ?? false,
            projectRoles: p.projectRoles ?? {},
          },
        };
      } catch (e) {
        if (e instanceof ApiError && (e.status === 401 || e.status === 403)) {
          return null;
        }
        throw e;
      }
    },
    staleTime: 60_000,
  });
}

export function useAuthMethods() {
  return useQuery({
    queryKey: ["auth", "methods"] as const,
    queryFn: getAuthMethods,
    staleTime: 5 * 60_000,
  });
}

// isSafeRedirect returns true for same-origin relative paths only.
// Rejects:
//   - empty / non-string
//   - protocol-relative ("//evil.com/x") — browsers treat as off-host
//   - absolute URLs (http://, https://, javascript:, data:, etc.)
//   - anything missing a leading "/"
// Used by the login flow to gate the ?next= bounce target.
function isSafeRedirect(s: unknown): s is string {
  if (typeof s !== "string" || s.length === 0) return false;
  if (s[0] !== "/") return false;
  if (s.length >= 2 && s[1] === "/") return false; // "//host" is off-origin
  if (s.includes("\\")) return false; // some browsers normalise \\ to //
  return true;
}

export function useLogin() {
  const qc = useQueryClient();
  const router = useRouter();
  return useMutation({
    mutationFn: (input: LoginInput) => loginApi(input),
    onSuccess: async (data) => {
      setJwt(data.access_token);
      // Identity boundary: start from a clean cache. If a previous user
      // signed out in this tab without a full reload, their cached
      // queries (projects, users, audit, tokens…) would otherwise be
      // served to this session until each one's next refetch. clear()
      // also makes any mounted session observer refetch with the new
      // cookie, so the old invalidateQueries call is subsumed.
      qc.clear();
      await qc.invalidateQueries({ queryKey: sessionQueryKey });
      const url = new URL(window.location.href);
      const raw = url.searchParams.get("next") ?? "/projects";
      // Open-redirect guard: only honour same-origin relative paths.
      // A bare `?next=https://attacker.example.com/phish` would
      // otherwise bounce a freshly-authenticated user off-domain
      // with their JWT in localStorage. Accept only paths that start
      // with a single "/" and don't open a protocol-relative URL via
      // "//host". Anything else collapses back to the safe default.
      const next = isSafeRedirect(raw) ? raw : "/projects";
      router.replace(next);
    },
  });
}

export function useSignOut() {
  const qc = useQueryClient();
  const router = useRouter();
  return () => {
    // Await the server-side cookie drop before clearing local state so
    // a fast follow-up login can't race the old session. clearJwt
    // swallows network errors, so .finally always runs.
    void clearJwt().finally(() => {
      // Identity boundary: wipe the ENTIRE query cache, not just the
      // session query. removeQueries(sessionQueryKey) alone left every
      // other cached query (projects, users, audit, tokens, SQL
      // results) readable by the next user of this browser until its
      // own refetch.
      qc.clear();
      router.replace("/login");
    });
  };
}

// useCan returns true when the current session carries the
// requested permission. Components use this to gate Save bars,
// destructive buttons, sensitive tabs, etc. Returns false during
// the initial session fetch so we err on the side of hiding stuff
// (better than flashing an unauthorized control then yanking it).
//
// useCan checks an INSTANCE-level permission against the JWT-baked perm
// set (settings:admin, user:write, audit:read, billing:read,
// system:update, settings:read). In role-system v2 these are the only
// perms in the token; PROJECT-scoped affordances must use
// useCanOnProject instead — useCan will (correctly) return false for a
// project perm because non-admins don't carry them in the JWT.
//
// Pass an array to OR multiple perms.
export function useCan(perm: string | string[]): boolean {
  const { data } = useSession();
  if (!data) return false;
  const have = data.session.permissions ?? [];
  const wants = Array.isArray(perm) ? perm : [perm];
  for (const w of wants) {
    if (have.includes(w)) return true;
  }
  return false;
}

// permsForProjectRole mirrors server auth.PermsForProjectRole — the
// project-scoped half of the matrix. Keep in lockstep with
// server-go/internal/auth/permissions.go.
function permsForProjectRole(role: "admin" | "editor" | "viewer" | ""): Set<string> {
  const read = ["project:read", "services:read", "addons:read"];
  const write = ["project:write", "services:write", "addons:write", "secrets:write"];
  const adminOnly = ["secrets:read", "shell:exec", "sql:read"];
  switch (role) {
    case "admin":
      return new Set([...read, ...write, ...adminOnly]);
    case "editor":
      return new Set([...read, ...write]);
    case "viewer":
      return new Set(read);
    default:
      return new Set();
  }
}

// useProjectRole returns the caller's effective role on a project
// ("admin" for instance admins via adminAll), or "" if the project is
// invisible to them.
export function useProjectRole(project: string): "admin" | "editor" | "viewer" | "" {
  const { data } = useSession();
  if (!data) return "";
  if (data.session.adminAll) return "admin";
  return data.session.projectRoles?.[project] ?? "";
}

// useCanOnProject gates a PROJECT-scoped affordance by resolving the
// caller's effective role on `project` and checking the role's perm set
// — the client-side analog of the server's requireProjectAccess +
// PermsForProjectRole. Use for Save/Deploy/Run/env/addon/SQL/shell
// buttons inside a project. Pass an array to OR.
export function useCanOnProject(project: string, perm: string | string[]): boolean {
  const role = useProjectRole(project);
  if (role === "") return false;
  const have = permsForProjectRole(role);
  const wants = Array.isArray(perm) ? perm : [perm];
  return wants.some((w) => have.has(w));
}

// Common permission strings — match what server/internal/auth/permissions.go
// emits. Re-declared here so consumers don't have to memorize the
// magic strings or chase typos.
export const Perms = {
  SettingsAdmin: "settings:admin",
  SettingsRead: "settings:read",
  AuditRead: "audit:read",
  UserWrite: "user:write",
  ProjectWrite: "project:write",
  ProjectRead: "project:read",
  ServicesWrite: "services:write",
  ServicesRead: "services:read",
  SecretsWrite: "secrets:write",
  SecretsRead: "secrets:read",
  SQLRead: "sql:read",
  ShellExec: "shell:exec",
  AddonsWrite: "addons:write",
  AddonsRead: "addons:read",
  SystemUpdate: "system:update",
} as const;

// usePending returns true when the session exists but the user has no
// usable access yet (awaiting an admin grant). Mirrors server
// auth.IsPending in role-system v2: an instance admin is never pending;
// anyone else is pending iff they can see zero projects. Drives the
// redirect to /awaiting-access.
//
// NOTE: must NOT key off permissions.length===0 anymore — in v2 every
// non-admin has an empty JWT perm set, which previously bounced every
// editor/viewer to awaiting-access even with valid project grants.
export function usePending(): boolean {
  const { data } = useSession();
  if (!data) return false;
  const s = data.session;
  if (s.adminAll) return false;
  return Object.keys(s.projectRoles ?? {}).length === 0;
}
