"use client";

import { useEffect, useState } from "react";
import { ChevronRight } from "lucide-react";
import { LoginForm } from "@/features/auth/components/LoginForm";
import { SocialButtons } from "@/features/auth/components/SocialButtons";
import { useAuthMethods } from "@/features/auth/hooks";
import { CopyButton } from "@/components/ui/copy-button";

const ADMIN_PASSWORD_CMD =
  "kubectl -n kuso get secret kuso-admin-credentials -o jsonpath='{.data.password}' | base64 -d";

export default function LoginPage() {
  // The instance host tells people with several kuso bookmarks (dev /
  // staging / prod) which one they're about to type credentials into.
  // Client-only because static export can't see request headers.
  const [host, setHost] = useState<string>("");
  const methods = useAuthMethods();
  const hasSSO = !!(methods.data?.github || methods.data?.oauth2);
  useEffect(() => {
    if (typeof window !== "undefined") setHost(window.location.host);
  }, []);

  return (
    <div>
      <h1 className="text-2xl font-semibold tracking-tight text-[var(--text-primary)]">Sign in to kuso</h1>
      <p className="mt-2 flex min-h-6 items-center gap-2 text-sm text-[var(--text-secondary)]">
        {host && (
          <>
            <span aria-hidden className="size-1.5 shrink-0 rounded-full bg-[var(--success)]" />
            <span className="sr-only">Instance:</span>
            <span className="truncate font-mono text-[13px] text-[var(--text-primary)]">{host}</span>
          </>
        )}
      </p>

      <div className="mt-8">
        <LoginForm />
        {hasSSO && (
          <>
            <div className="my-5 flex items-center gap-3 text-xs text-[var(--text-tertiary)]">
              <span className="h-px flex-1 bg-[var(--border-subtle)]" />
              or
              <span className="h-px flex-1 bg-[var(--border-subtle)]" />
            </div>
            <SocialButtons />
          </>
        )}
      </div>

      <details className="group mt-8 border-t border-[var(--border-subtle)] pt-4 text-sm">
        <summary className="flex cursor-pointer list-none items-center gap-1 rounded-sm text-[13px] text-[var(--text-secondary)] select-none hover:text-[var(--text-primary)] [&::-webkit-details-marker]:hidden">
          <ChevronRight aria-hidden className="size-3.5 transition-transform group-open:rotate-90" />
          Can&apos;t sign in?
        </summary>
        <div className="mt-3 space-y-3 text-[13px] leading-relaxed text-[var(--text-secondary)]">
          <p>
            On a fresh install the user is <span className="font-mono text-[var(--text-primary)]">admin</span>{" "}
            and the installer printed the password when it finished. It&apos;s also stored in the cluster:
          </p>
          <div className="flex items-start gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] py-1.5 pr-1 pl-3">
            <code className="min-w-0 flex-1 py-0.5 font-mono text-xs break-all text-[var(--text-primary)]">
              {ADMIN_PASSWORD_CMD}
            </code>
            <CopyButton value={ADMIN_PASSWORD_CMD} label="Copy command" size="xs" />
          </div>
          <p>
            Any other user: ask an admin to run{" "}
            <span className="font-mono text-[var(--text-primary)]">kuso user set-password</span>.
          </p>
        </div>
      </details>
    </div>
  );
}
