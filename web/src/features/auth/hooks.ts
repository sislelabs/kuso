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

export function useLogin() {
  const qc = useQueryClient();
  const router = useRouter();
  return useMutation({
    mutationFn: (input: LoginInput) => loginApi(input),
    onSuccess: async (data) => {
      setJwt(data.access_token);
      await qc.invalidateQueries({ queryKey: sessionQueryKey });
      const url = new URL(window.location.href);
      const next = url.searchParams.get("next") ?? "/projects";
      router.replace(next);
    },
  });
}

export function useSignOut() {
  const qc = useQueryClient();
  const router = useRouter();
  return () => {
    clearJwt();
    qc.removeQueries({ queryKey: sessionQueryKey });
    router.replace("/login");
  };
}

// useCan returns true when the current session carries the
// requested permission. Components use this to gate Save bars,
// destructive buttons, sensitive tabs, etc. Returns false during
// the initial session fetch so we err on the side of hiding stuff
// (better than flashing an unauthorized control then yanking it).
//
// Pass an array to OR multiple perms ("settings:admin OR
// settings:read" pattern for read-mostly UIs).
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
  AddonsWrite: "addons:write",
  AddonsRead: "addons:read",
  SystemUpdate: "system:update",
} as const;

// usePending returns true when the session exists but the user has
// no perms (pending admin approval). Drives the redirect to
// /awaiting-access.
export function usePending(): boolean {
  const { data } = useSession();
  if (!data) return false;
  return (data.session.permissions ?? []).length === 0;
}
