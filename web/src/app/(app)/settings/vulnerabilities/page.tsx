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
import { SettingsPage } from "@/components/settings/SettingsPage";
import { SettingsNotice } from "@/components/settings/SettingsSection";

function ScanSettings() {
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
    <SettingsPage
      title="Vulnerability scanning"
      description="kuso scans every image it runs for known CVEs with Trivy: service images after each build and nightly, addon and platform images nightly."
      actions={
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
      }
    >
      <ScanSettings />

      <section className="space-y-3">
        <div>
          <h2 className="text-base font-semibold tracking-tight">Platform images</h2>
          <p className="mt-1 text-sm text-[var(--text-secondary)]">
            kuso-server, operator, registry, buildkitd, traefik and the rest of the control plane. Click a row for its
            findings.
          </p>
        </div>
        <PlatformImages />
      </section>
    </SettingsPage>
  );
}

export default function VulnerabilityScanningPage() {
  const isAdmin = useCan(Perms.SettingsAdmin);
  if (!isAdmin) {
    return (
      <SettingsPage title="Vulnerability scanning">
        <SettingsNotice tone="warning">Vulnerability scanning settings are admin-only.</SettingsNotice>
      </SettingsPage>
    );
  }
  return <VulnScanningAdmin />;
}
