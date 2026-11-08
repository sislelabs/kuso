"use client";

import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useCan, Perms } from "@/features/auth";
import { settingsPatch, updateClusterSettings } from "@/features/cluster-config/api";
import { DefaultPodSizeSection } from "@/components/settings/DefaultPodSizeSection";
import { InstanceInfoSection } from "@/components/settings/InstanceInfoSection";
import { PodSizePresetsSection } from "@/components/settings/PodSizePresetsSection";
import { RunpacksSection } from "@/components/settings/RunpacksSection";
import { instanceInfoQueryKey } from "@/features/instance-config";
import { toast } from "sonner";
import { Save } from "lucide-react";
import { SettingsPage, SettingsPageLoading } from "@/components/settings/SettingsPage";
import { SettingsField, SettingsNotice, SettingsSection } from "@/components/settings/SettingsSection";

interface SettingsResp {
  settings: Record<string, unknown>;
}

// Only keys the server actually reads belong here. baseDomain, ingressClass
// and letsEncryptEmail used to be listed but nothing consumed them, so the
// form said "saved" for settings with no effect.
const FIELDS: { key: string; label: string; hint: string; placeholder?: string }[] = [
  {
    key: "clusterissuer",
    label: "ClusterIssuer",
    hint: "The issuer /api/config/clusterissuer reports. Environment certificates still use letsencrypt-prod.",
    placeholder: "e.g. letsencrypt-prod",
  },
];

export default function ClusterConfigPage() {
  const canEdit = useCan(Perms.SettingsAdmin);
  const qc = useQueryClient();
  const settings = useQuery({
    queryKey: ["admin", "settings"],
    queryFn: async () => (await api<SettingsResp>("/api/config")).settings ?? {},
  });
  const [draft, setDraft] = useState<Record<string, unknown>>({});
  const [rawJson, setRawJson] = useState("");
  const [rawError, setRawError] = useState<string | null>(null);
  // The raw spec can hold registry credentials and the OAuth client
  // secret, so it stays collapsed until asked for.
  const [showRaw, setShowRaw] = useState(false);

  useEffect(() => {
    if (settings.data) {
      setDraft(settings.data);
      setRawJson(JSON.stringify(settings.data, null, 2));
    }
  }, [settings.data]);

  const save = useMutation({
    mutationFn: (next: Record<string, unknown>) =>
      updateClusterSettings(settingsPatch(settings.data ?? {}, next)),
    onSuccess: () => {
      toast.success("Cluster config saved");
      qc.invalidateQueries({ queryKey: ["admin", "settings"] });
      qc.invalidateQueries({ queryKey: instanceInfoQueryKey });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Save failed"),
  });

  if (settings.isPending) {
    return <SettingsPageLoading title="Cluster config" rows={3} />;
  }

  const setField = (key: string, val: string) => {
    setDraft((cur) => {
      const next = { ...cur, [key]: val };
      setRawJson(JSON.stringify(next, null, 2));
      return next;
    });
  };

  const onSaveFriendly = () => {
    if (!canEdit) {
      toast.error("settings:admin permission required");
      return;
    }
    save.mutate(draft);
  };
  const onSaveRaw = () => {
    if (!canEdit) {
      toast.error("settings:admin permission required");
      return;
    }
    try {
      const parsed = JSON.parse(rawJson);
      if (typeof parsed !== "object" || parsed === null) {
        setRawError("must be a JSON object");
        return;
      }
      setRawError(null);
      setDraft(parsed);
      save.mutate(parsed);
    } catch (e) {
      setRawError(e instanceof Error ? e.message : "invalid JSON");
    }
  };

  return (
    <SettingsPage
      title="Cluster config"
      description="Cluster-wide knobs stored on the Kuso CR. The server re-reads them on every reload."
    >
      {!canEdit && (
        <SettingsNotice tone="neutral" title="Read-only">
          Changing cluster config needs the <code className="font-mono text-xs">settings:admin</code> permission.
        </SettingsNotice>
      )}

      <InstanceInfoSection />

      <SettingsSection
        title="Certificates"
        footer={
          <Button size="sm" onClick={onSaveFriendly} disabled={!canEdit || save.isPending}>
            <Save className="h-3 w-3" />
            {save.isPending ? "Saving…" : "Save"}
          </Button>
        }
      >
        {FIELDS.map((f) => (
          <SettingsField key={f.key} label={f.label} hint={f.hint} htmlFor={`cfg-${f.key}`}>
            <Input
              id={`cfg-${f.key}`}
              value={String(draft[f.key] ?? "")}
              onChange={(e) => setField(f.key, e.target.value)}
              placeholder={f.placeholder}
              className="font-mono"
              disabled={!canEdit}
              spellCheck={false}
            />
          </SettingsField>
        ))}
      </SettingsSection>

      <DefaultPodSizeSection canEdit={canEdit} />

      <PodSizePresetsSection canEdit={canEdit} />

      <RunpacksSection canEdit={canEdit} />

      <SettingsSection
        title="Raw spec"
        description="Edit the entire Kuso CR spec as JSON. It's validated before saving and may contain secrets (registry credentials, OAuth client secret)."
        actions={
          <Button size="sm" variant="outline" onClick={() => setShowRaw((v) => !v)} aria-expanded={showRaw}>
            {showRaw ? "Hide" : "Show"}
          </Button>
        }
        footer={
          showRaw ? (
            <>
              <Button
                size="sm"
                variant="outline"
                onClick={() => setRawJson(JSON.stringify(draft, null, 2))}
                disabled={!canEdit}
              >
                Sync from form
              </Button>
              <Button size="sm" onClick={onSaveRaw} disabled={!canEdit || save.isPending}>
                <Save className="h-3 w-3" />
                {save.isPending ? "Saving…" : "Save raw"}
              </Button>
            </>
          ) : undefined
        }
      >
        {showRaw ? (
          <SettingsField label="Spec JSON" htmlFor="cfg-raw" error={rawError ? `Parse error: ${rawError}` : undefined}>
            <textarea
              id="cfg-raw"
              value={rawJson}
              onChange={(e) => setRawJson(e.target.value)}
              spellCheck={false}
              rows={Math.min(28, Math.max(8, rawJson.split("\n").length))}
              disabled={!canEdit}
              className="w-full resize-y rounded-sm border border-border bg-[var(--bg-primary)] p-3 font-mono text-xs text-[var(--text-primary)] outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:opacity-50"
            />
          </SettingsField>
        ) : null}
      </SettingsSection>
    </SettingsPage>
  );
}
