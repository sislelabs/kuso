"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";

// /settings/roles → /settings/groups. The roles UI was retired in
// v0.5.7; tenancy now lives entirely on Groups (instance role + project
// memberships). This redirect lets stale bookmarks land somewhere
// useful instead of a generic 404.
export default function RolesRedirect() {
  const router = useRouter();
  useEffect(() => {
    router.replace("/settings/groups");
  }, [router]);
  return (
    <div className="p-6 text-sm text-[var(--text-tertiary)]">
      Redirecting to Groups…
    </div>
  );
}
