"use client";

import { useEffect } from "react";
import Link from "next/link";
import { CheckCircle2 } from "lucide-react";
import { announceGithubInstalled } from "@/features/github";

export default function GithubInstalledPage() {
  useEffect(() => {
    announceGithubInstalled();
  }, []);
  return (
    <div className="space-y-3 text-center">
      <CheckCircle2 className="mx-auto h-8 w-8 text-[var(--success)]" />
      <h1 className="font-heading text-xl font-semibold tracking-tight">GitHub App installed</h1>
      <p className="text-sm text-[var(--text-secondary)]">You can close this tab.</p>
      <Link
        href="/projects"
        className="inline-block font-mono text-[11px] text-[var(--text-tertiary)] underline hover:text-[var(--text-secondary)]"
      >
        go to projects
      </Link>
    </div>
  );
}
