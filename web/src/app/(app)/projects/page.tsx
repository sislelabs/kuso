"use client";

import { useSession, useSignOut } from "@/features/auth";

export default function ProjectsPlaceholder() {
  const { data } = useSession();
  const signOut = useSignOut();
  return (
    <div className="p-8">
      <h1 className="font-heading text-2xl font-semibold">Projects</h1>
      <p className="mt-2 text-sm text-[var(--text-secondary)]">
        Welcome, {data?.user.name ?? "user"}. Phase B will replace this placeholder.
      </p>
      <button onClick={signOut} className="mt-4 text-sm underline">
        sign out
      </button>
    </div>
  );
}
