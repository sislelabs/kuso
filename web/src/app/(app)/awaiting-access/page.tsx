"use client";

import { useSession, useSignOut } from "@/features/auth";
import { Button } from "@/components/ui/button";
import { ShieldAlert, LogOut } from "lucide-react";

// Awaiting-access page. Authenticated users with no perms land here
// instead of bouncing off every guarded route. We deliberately keep
// it bare: name, email, "ask an admin," sign-out. The admin's
// contact info is intentionally not surfaced — operators can
// customize this in a later release once we have an instance-config
// surface that can carry HTML / contact links.
export default function AwaitingAccessPage() {
  const { data } = useSession();
  const signOut = useSignOut();

  return (
    <div className="mx-auto max-w-lg p-8 lg:p-12">
      <div className="rounded-md border border-amber-500/30 bg-amber-500/5 p-6">
        <div className="flex items-start gap-3">
          <ShieldAlert className="h-5 w-5 shrink-0 text-amber-400" />
          <div>
            <h1 className="font-heading text-base font-semibold tracking-tight">
              Awaiting access
            </h1>
            <p className="mt-2 text-sm text-[var(--text-secondary)]">
              Your account exists, but no admin has granted it a group yet. Ask the operator
              of this kuso instance to add you to a project group with the access you need.
            </p>
            <div className="mt-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-3 font-mono text-[11px]">
              <div className="text-[var(--text-tertiary)]">signed in as</div>
              <div className="mt-0.5 text-[var(--text-primary)]">
                {data?.user.name || data?.user.email || "unknown"}
              </div>
              {data?.user.email && data?.user.name && (
                <div className="mt-0.5 text-[var(--text-tertiary)]">{data.user.email}</div>
              )}
            </div>
          </div>
        </div>
        <div className="mt-4 flex justify-end">
          <Button variant="outline" size="sm" onClick={signOut}>
            <LogOut className="h-3 w-3" />
            Sign out
          </Button>
        </div>
      </div>
    </div>
  );
}
