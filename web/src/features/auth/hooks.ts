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
