"use client";

import { useState } from "react";
import { cn } from "@/lib/utils";
import { useUserDirectory, type ResolvedUser } from "@/features/users";

// UserBadge shows a user id as avatar + username when the directory can
// resolve it, otherwise a short mono id with the full id on hover.
export function UserBadge({ id, className }: { id: string; className?: string }) {
  const { resolve } = useUserDirectory();
  if (!id) {
    return <span className={cn("text-[var(--text-tertiary)]", className)}>system</span>;
  }
  const u = resolve(id);
  if (!u) {
    return (
      <span className={cn("font-mono text-xs text-[var(--text-secondary)]", className)} title={`Unknown user ${id}`}>
        {id.length > 12 ? `${id.slice(0, 8)}…` : id}
      </span>
    );
  }
  return (
    <span className={cn("inline-flex min-w-0 items-center gap-1.5", className)} title={id}>
      <UserAvatar user={u} />
      <span className="truncate text-[var(--text-primary)]">{u.username}</span>
    </span>
  );
}

export function UserAvatar({ user, size = 20 }: { user: Pick<ResolvedUser, "username" | "image">; size?: number }) {
  const [broken, setBroken] = useState(false);
  const initials = user.username.slice(0, 2).toUpperCase();
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
      {initials}
    </span>
  );
}
