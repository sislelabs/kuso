"use client";

import { ScanSearch } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { VulnSettingsForm } from "@/components/vulns/VulnSettingsForm";
import { PlatformImages } from "@/components/vulns/PlatformImages";
import { useCan, Perms } from "@/features/auth";
import { useScanAllVulns, useVulnScanSettings } from "@/features/vulns";

function SettingsSection() {
  const settings = useVulnScanSettings();
  if (settings.isError) {
    return (
      <QueryErrorState
        what="vulnerability scanning settings"
        error={settings.error}
        onRetry={() => void settings.refetch()}
      />
    );
  }
  if (!settings.data) return <Skeleton className="h-64 w-full" />;
  return <VulnSettingsForm initial={settings.data} />;
}

function VulnScanningAdmin() {
  const scanAll = useScanAllVulns();
  return (
    <div className="mx-auto max-w-5xl space-y-8 p-6 lg:p-8">
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="font-heading text-2xl font-semibold tracking-tight">Vulnerability scanning</h1>
          <p className="mt-1 max-w-2xl text-sm text-[var(--text-secondary)]">
            kuso scans every image it runs for known CVEs with Trivy: service images after each build and nightly, addon
            and platform images nightly.
          </p>
        </div>
        <Button
          variant="neutral"
          disabled={scanAll.isPending}
          onClick={() =>
            scanAll.mutate(undefined, {
              onSuccess: (r) => toast.success(`Queued ${r.queued} scan${r.queued === 1 ? "" : "s"}`),
            })
          }
        >
          <ScanSearch />
          {scanAll.isPending ? "Queueing…" : "Scan everything now"}
        </Button>
      </header>

      <SettingsSection />

      <section className="space-y-3">
        <div>
          <h2 className="text-sm font-semibold">Platform images</h2>
          <p className="text-xs text-[var(--text-secondary)]">
            kuso-server, operator, registry, buildkitd, traefik and the rest of the control plane. Click a row for its
            findings.
          </p>
        </div>
        <PlatformImages />
      </section>
    </div>
  );
}

export default function VulnerabilityScanningPage() {
  const isAdmin = useCan(Perms.SettingsAdmin);
  if (!isAdmin) {
    return (
      <div className="mx-auto max-w-3xl p-6 lg:p-8">
        <p className="rounded-md border border-[var(--warning)]/30 bg-[var(--warning-subtle)] p-4 text-sm text-[var(--warning)]">
          Vulnerability scanning settings are admin-only.
        </p>
      </div>
    );
  }
  return <VulnScanningAdmin />;
}
