"use client";

import { useState } from "react";
import { Cog } from "lucide-react";
import { cn } from "@/lib/utils";
import { useUserDirectory, type ResolvedUser } from "@/features/users";
import { actorInitials, actorLabel, isSystemActor } from "./actor";
import { looksLikeUserId } from "./audit-text";

// UserBadge renders an audit actor as avatar + name, the same shape for
// every kind: a directory user gets their avatar (or initials), a
// system actor a cog glyph, an unresolvable id initials + a short id.
export function UserBadge({ id, className }: { id: string; className?: string }) {
  const { resolve } = useUserDirectory();
  const u = isSystemActor(id) ? undefined : resolve(id);
  if (u) {
    return (
      <span className={cn("inline-flex min-w-0 items-center gap-1.5", className)} title={id}>
        <UserAvatar user={u} />
        <span className="truncate text-[var(--text-primary)]">{u.username}</span>
      </span>
    );
  }
  if (isSystemActor(id)) {
    return (
      <span
        className={cn("inline-flex min-w-0 items-center gap-1.5", className)}
        title="Automated action by kuso, not a user"
      >
        <span
          aria-hidden
          className="inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-[var(--bg-tertiary)] text-[var(--text-secondary)]"
        >
          <Cog className="h-3 w-3" />
        </span>
        <span className="truncate text-[var(--text-secondary)]">{actorLabel(id)}</span>
      </span>
    );
  }
  // An opaque id the directory can't resolve (a removed user, or a
  // viewer who can't list users): say so instead of printing the hash.
  const opaque = looksLikeUserId(id);
  return (
    <span className={cn("inline-flex min-w-0 items-center gap-1.5", className)} title={`User id ${id}`}>
      <UserAvatar user={{ username: opaque ? "?" : id }} />
      <span className={cn("truncate text-[var(--text-secondary)]", !opaque && "font-mono text-xs")}>
        {opaque ? "Unknown user" : actorLabel(id)}
      </span>
    </span>
  );
}

export function UserAvatar({ user, size = 20 }: { user: Pick<ResolvedUser, "username" | "image">; size?: number }) {
  const [broken, setBroken] = useState(false);
  if (user.image && !broken) {
    return (
      <img
        src={user.image}
        alt=""
        width={size}
        height={size}
        onError={() => setBroken(true)}
        className="shrink-0 rounded-full object-cover"
        style={{ width: size, height: size }}
      />
    );
  }
  return (
    <span
      aria-hidden
      className="inline-flex shrink-0 items-center justify-center rounded-full bg-[var(--bg-tertiary)] text-xs font-semibold text-[var(--text-secondary)]"
      style={{ width: size, height: size }}
    >
      {actorInitials(user.username)}
    </span>
  );
}
