"use client";

import { useState } from "react";
import { Github, Puzzle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { AddOauthAppDialog } from "../AddOauthAppDialog";
import { Section, Code } from "./_primitives";

// IntegrationsSection holds the third-party app helpers. "Sign in with
// GitHub" registers an OAuth app for this service's URL and stores its
// client ID and secret as variables.
export function IntegrationsSection({
  project,
  service,
  host,
  canWrite,
}: {
  project: string;
  service: string;
  // The edited environment's public host; the OAuth callback points at it.
  host: string;
  canWrite: boolean;
}) {
  const [open, setOpen] = useState(false);
  return (
    <Section
      id="integrations"
      title="Integrations"
      icon={Puzzle}
      scope="none"
      description="Set up third-party sign-in for the app this service runs."
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="text-[13px] font-medium text-[var(--text-primary)]">Sign in with GitHub</div>
          <p className="mt-0.5 text-xs leading-relaxed text-[var(--text-secondary)]">
            Registers a GitHub OAuth app whose callback is this service&apos;s URL and saves its client ID and secret
            as variables (<Code>GITHUB_CLIENT_ID</Code>, <Code>GITHUB_CLIENT_SECRET</Code>).
          </p>
          {!host && (
            <p className="mt-1 text-xs text-[var(--text-tertiary)]">Needs a public URL; this environment has none yet.</p>
          )}
        </div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => setOpen(true)}
          disabled={!host || !canWrite}
          disabledReason={!canWrite ? "Requires editor access on this project" : !host ? "This environment has no public URL" : undefined}
        >
          <Github className="h-3.5 w-3.5" />
          Set up…
        </Button>
      </div>
      {host && <AddOauthAppDialog open={open} onOpenChange={setOpen} project={project} service={service} serviceHost={host} />}
    </Section>
  );
}
