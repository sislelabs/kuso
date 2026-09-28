"use client";

import { useEffect, useState } from "react";
import { Separator } from "@/components/ui/separator";
import { LoginForm } from "@/features/auth/components/LoginForm";
import { SocialButtons } from "@/features/auth/components/SocialButtons";
import { useAuthMethods } from "@/features/auth/hooks";

export default function LoginPage() {
  // Show the kuso instance hostname so users with multiple kuso
  // bookmarks (dev / staging / prod) know which one they're on
  // before they type credentials. Client-only because static export
  // can't see request headers.
  const [host, setHost] = useState<string>("");
  const methods = useAuthMethods();
  const hasSSO = !!(methods.data?.github || methods.data?.oauth2);
  useEffect(() => {
    if (typeof window !== "undefined") setHost(window.location.host);
  }, []);
  return (
    <div className="space-y-4">
      <div>
        <h1 className="font-heading text-xl font-semibold tracking-tight">
          Sign in
        </h1>
        <p className="text-sm text-[var(--text-secondary)]">
          to{" "}
          {host ? (
            <span className="font-mono text-[var(--text-primary)]">{host}</span>
          ) : (
            "your kuso instance"
          )}
        </p>
      </div>
      <LoginForm />
      {hasSSO && (
        <>
          <div className="flex items-center gap-2">
            <Separator className="flex-1" />
            <span className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
              or
            </span>
            <Separator className="flex-1" />
          </div>
          <SocialButtons />
        </>
      )}
      <p className="text-[11px] leading-relaxed text-[var(--text-tertiary)]">
        First sign-in: the user is <span className="font-mono">admin</span> and the installer
        printed the password when it finished. Lost it? The installer&apos;s copy is in the
        cluster:{" "}
        <code className="font-mono break-all">
          kubectl -n kuso get secret kuso-admin-credentials -o jsonpath=&apos;{"{.data.password}"}&apos; | base64 -d
        </code>
        . An admin can reset any other user&apos;s password with{" "}
        <code className="font-mono">kuso user set-password</code>.
      </p>
    </div>
  );
}
