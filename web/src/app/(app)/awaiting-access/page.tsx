"use client";

import { useEffect } from "react";
import { useSession, useSignOut } from "@/features/auth";
import { Button } from "@/components/ui/button";
import { ShieldAlert, LogOut, RefreshCw } from "lucide-react";
import { PageContainer } from "@/components/shared/PageContainer";

// Awaiting-access page. Authenticated users with no perms land here
// instead of bouncing off every guarded route. We deliberately keep
// it bare: name, email, "ask an admin," sign-out. The admin's
// contact info is intentionally not surfaced — operators can
// customize this in a later release once we have an instance-config
// surface that can carry HTML / contact links.
export default function AwaitingAccessPage() {
  const { data, refetch, isFetching } = useSession();
  const signOut = useSignOut();

  // Re-check every 30s while visible; AuthGate moves the user on once
  // an admin grants a group.
  useEffect(() => {
    const id = window.setInterval(() => {
      if (document.visibilityState === "visible") void refetch();
    }, 30_000);
    return () => window.clearInterval(id);
  }, [refetch]);

  return (
    <PageContainer width="narrow">
      <div className="mx-auto max-w-md pt-10 text-center">
        <div
          aria-hidden
          className="mx-auto mb-4 flex size-11 items-center justify-center rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-elevated)] text-[var(--text-secondary)] shadow-[var(--shadow-sm)]"
        >
          <ShieldAlert className="size-5" />
        </div>
        <h1 className="text-xl font-semibold tracking-tight">You&apos;re signed in, but have no access yet</h1>
        <p className="mt-2 text-sm text-[var(--text-secondary)]">
          Ask the admin of this kuso instance to add you to a group. This page checks again every
          30 seconds and moves you on once access is granted.
        </p>
        <div className="mt-6 rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-4 py-3 text-left text-sm">
          <p className="text-xs text-[var(--text-tertiary)]">Signed in as</p>
          <p className="mt-0.5 font-medium text-[var(--text-primary)]">
            {data?.user.name || data?.user.email || "unknown"}
          </p>
          {data?.user.email && data?.user.name && (
            <p className="text-[13px] text-[var(--text-secondary)]">{data.user.email}</p>
          )}
        </div>
        <div className="mt-6 flex justify-center gap-2">
          <Button variant="primary" size="sm" onClick={() => void refetch()} loading={isFetching}>
            {!isFetching && <RefreshCw aria-hidden />}
            {isFetching ? "Checking…" : "Check again"}
          </Button>
          <Button variant="secondary" size="sm" onClick={signOut}>
            <LogOut aria-hidden />
            Sign out
          </Button>
        </div>
      </div>
    </PageContainer>
  );
}
