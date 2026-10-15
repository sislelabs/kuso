"use client";

import { useState } from "react";
import { Lock, Plus, X } from "lucide-react";
import { toast } from "sonner";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { useEnvDomainMutations } from "@/features/services";
import { Row } from "./_primitives";

// WildcardDomainsRow manages the env's wildcard hosts. Unlike the
// custom-domain list above (staged and saved with the form), these
// apply immediately: each one names a pre-provisioned TLS Secret, and
// the server validates both together.
export function WildcardDomainsRow({
  project,
  service,
  envName,
  domains,
  canWrite,
}: {
  project: string;
  service: string;
  envName: string;
  domains: { host: string; tlsSecret?: string }[];
  canWrite: boolean;
}) {
  const { add, remove } = useEnvDomainMutations(project, service, envName);
  const [host, setHost] = useState("");
  const [secret, setSecret] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);

  const onAdd = async () => {
    const h = host.trim().toLowerCase();
    const s = secret.trim();
    if (!h.startsWith("*.")) {
      setError("Wildcard hosts start with *. — e.g. *.example.com");
      return;
    }
    if (!s) {
      setError("Name the TLS Secret that holds the wildcard certificate");
      return;
    }
    setError(null);
    try {
      await add.mutateAsync({ host: h, tlsSecret: s });
      toast.success(`${h} added`);
      setHost("");
      setSecret("");
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to add domain");
    }
  };

  const onRemove = async () => {
    if (!removing) return;
    try {
      await remove.mutateAsync(removing);
      toast.success(`${removing} removed`);
      setRemoving(null);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to remove domain");
      setRemoving(null);
    }
  };

  return (
    <Row
      label="wildcard domains"
      hint={`${envName} environment only · served with your own TLS Secret (mint it via a cert-manager DNS-01 Certificate) · applies immediately`}
      control={
        <div className="flex w-full max-w-[420px] flex-col gap-1.5">
          {domains.map((d) => (
            <div key={d.host} className="flex items-center gap-1.5 font-mono text-[12px]">
              <Lock className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
              <span className="min-w-0 flex-1 truncate">{d.host}</span>
              <span className="truncate text-[10px] text-[var(--text-tertiary)]">{d.tlsSecret}</span>
              {canWrite && (
                <button
                  type="button"
                  onClick={() => setRemoving(d.host)}
                  aria-label={`Remove ${d.host}`}
                  className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-red-400"
                >
                  <X className="h-3.5 w-3.5" />
                </button>
              )}
            </div>
          ))}
          {canWrite && (
            <div className="flex items-center gap-1.5">
              <Input
                value={host}
                onChange={(e) => setHost(e.target.value)}
                placeholder="*.example.com"
                aria-label="Wildcard host"
                spellCheck={false}
                className="h-7 min-w-0 flex-1 font-mono text-[12px]"
              />
              <Input
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
                placeholder="wildcard-example-tls"
                aria-label="TLS secret name"
                spellCheck={false}
                className="h-7 min-w-0 flex-1 font-mono text-[12px]"
              />
              <Button variant="outline" size="xs" type="button" onClick={onAdd} disabled={add.isPending}>
                <Plus className="h-3 w-3" />
                {add.isPending ? "Adding…" : "Add"}
              </Button>
            </div>
          )}
          {error && <p className="font-mono text-[10px] text-[var(--error)]">{error}</p>}
          <ConfirmDialog
            open={removing !== null}
            title="Remove wildcard domain?"
            body={
              <>
                <span className="font-mono">{removing}</span> stops routing to this service in{" "}
                <span className="font-mono">{envName}</span>. The TLS Secret is left in place.
              </>
            }
            confirmLabel="Remove"
            pending={remove.isPending}
            onConfirm={onRemove}
            onCancel={() => setRemoving(null)}
          />
        </div>
      }
    />
  );
}
