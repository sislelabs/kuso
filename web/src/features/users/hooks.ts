"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { Perms, useCan, useSession } from "@/features/auth";
import { listUserDirectory } from "./api";

export interface ResolvedUser {
  id: string;
  username: string;
  image?: string | null;
}

// Shares the users page's cache key; both store the raw list.
export const userDirectoryQueryKey = ["admin", "users"] as const;

// useUserDirectory resolves user ids (as stored in audit rows, mutes,
// tokens) to display names. Without list permission it still knows the
// signed-in user, so "you" resolves for everyone; other ids stay as ids.
export function useUserDirectory(): {
  resolve: (id: string) => ResolvedUser | null;
  canList: boolean;
} {
  const canList = useCan([Perms.SettingsAdmin, Perms.UserWrite]);
  const session = useSession();
  const list = useQuery({
    queryKey: userDirectoryQueryKey,
    queryFn: listUserDirectory,
    enabled: canList,
    staleTime: 5 * 60_000,
    retry: false,
  });
  const me = session.data?.user;
  return useMemo(() => {
    const byId = new Map<string, ResolvedUser>();
    for (const u of list.data ?? []) byId.set(u.id, { id: u.id, username: u.username });
    if (me?.id) {
      const prev = byId.get(me.id);
      byId.set(me.id, { id: me.id, username: prev?.username ?? me.name, image: me.image });
    }
    return { resolve: (id: string) => byId.get(id) ?? null, canList };
  }, [list.data, me, canList]);
}
