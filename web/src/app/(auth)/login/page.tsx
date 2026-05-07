"use client";

import { useEffect, useState } from "react";
import { Separator } from "@/components/ui/separator";
import { LoginForm } from "@/features/auth/components/LoginForm";
import { SocialButtons } from "@/features/auth/components/SocialButtons";

export default function LoginPage() {
  // Show the kuso instance hostname so users with multiple kuso
  // bookmarks (dev / staging / prod) know which one they're on
  // before they type credentials. Client-only because static export
  // can't see request headers.
  const [host, setHost] = useState<string>("");
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
      <div className="flex items-center gap-2">
        <Separator className="flex-1" />
        <span className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          or
        </span>
        <Separator className="flex-1" />
      </div>
      <SocialButtons />
    </div>
  );
}
