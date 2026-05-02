"use client";

import { useEffect, useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Separator } from "@/components/ui/separator";
import { Github, Plus, Trash2, Rocket, ArrowRight, Check } from "lucide-react";
import { toast } from "sonner";
import {
  useInstallURL,
  useInstallations,
  useDetectRuntime,
  useScanAddons,
  type GithubRepo,
  type AddonSuggestion,
  type DetectRuntimeResponse,
} from "@/features/github";
import { useCreateProject } from "@/features/projects";
import { api, ApiError } from "@/lib/api-client";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";
import { AddonIcon, addonLabel } from "@/components/addon/AddonIcon";
import { cn } from "@/lib/utils";

interface ServiceRow {
  id: string; // local-only
  name: string;
  path: string;
  runtime?: string;
  port?: number;
  reason?: string;
  detecting?: boolean;
}

interface AddonRow {
  kind: string;
  enabled: boolean;
  reason?: string;
}

const ADDON_KINDS: { kind: string; suggested?: boolean }[] = [
  { kind: "postgres" },
  { kind: "redis" },
  { kind: "mongodb" },
  { kind: "mysql" },
  { kind: "rabbitmq" },
  { kind: "memcached" },
  { kind: "clickhouse" },
  { kind: "elasticsearch" },
  { kind: "kafka" },
  { kind: "cockroachdb" },
  { kind: "couchdb" },
];

function uid() {
  return Math.random().toString(36).slice(2, 9);
}

function ownerRepoFromFull(full: string): { owner: string; repo: string } {
  const [owner, repo] = full.split("/");
  return { owner: owner ?? "", repo: repo ?? "" };
}

function serviceNameFromRepo(full: string): string {
  return full.split("/")[1] ?? "service";
}

export default function NewProjectPage() {
  const router = useRouter();
  const installURL = useInstallURL();
  const installs = useInstallations();
  const detect = useDetectRuntime();
  const scan = useScanAddons();
  const create = useCreateProject();

  const [name, setName] = useState("");
  const [picked, setPicked] = useState<{ installationId: number; repo: GithubRepo } | null>(null);
  const [services, setServices] = useState<ServiceRow[]>([]);
  const [addons, setAddons] = useState<AddonRow[]>(
    ADDON_KINDS.map((a) => ({ kind: a.kind, enabled: false }))
  );
  const [submitting, setSubmitting] = useState(false);
  const [repoQuery, setRepoQuery] = useState("");

  const allRepos = useMemo(() => {
    return (installs.data ?? []).flatMap((inst) =>
      inst.repositories.map((r) => ({ installationId: inst.id, repo: r, owner: inst.accountLogin }))
    );
  }, [installs.data]);

  const filteredRepos = useMemo(() => {
    const q = repoQuery.trim().toLowerCase();
    if (!q) return allRepos;
    return allRepos.filter(({ repo }) => repo.fullName.toLowerCase().includes(q));
  }, [allRepos, repoQuery]);

  // When a repo is picked, prefill name and run detect+scan in parallel.
  useEffect(() => {
    if (!picked) return;
    const { installationId, repo } = picked;
    const { owner, repo: name2 } = ownerRepoFromFull(repo.fullName);

    if (!name) setName(serviceNameFromRepo(repo.fullName));

    // One service detected at root by default. Phase F doesn't auto-walk
    // the repo for monorepo subpaths — user can add rows manually.
    const sid = uid();
    setServices([{ id: sid, name: serviceNameFromRepo(repo.fullName), path: "", detecting: true }]);

    detect.mutateAsync({ installationId, owner, repo: name2, branch: repo.defaultBranch, path: "" })
      .then((res) => {
        setServices((prev) =>
          prev.map((s) =>
            s.id === sid
              ? { ...s, detecting: false, runtime: res.runtime, port: res.port, reason: res.reason }
              : s
          )
        );
      })
      .catch(() => {
        setServices((prev) => prev.map((s) => (s.id === sid ? { ...s, detecting: false } : s)));
      });

    scan.mutateAsync({ installationId, owner, repo: name2, branch: repo.defaultBranch, path: "" })
      .then((res) => {
        const sug = new Map<string, string>();
        (res.suggestions ?? []).forEach((x: AddonSuggestion) => sug.set(x.kind, x.reason));
        setAddons((prev) =>
          prev.map((a) => ({ ...a, enabled: sug.has(a.kind), reason: sug.get(a.kind) }))
        );
      })
      .catch(() => { /* best-effort */ });
  }, [picked]);

  const onAddService = () => {
    setServices((prev) => [...prev, { id: uid(), name: "", path: "", runtime: "dockerfile" }]);
  };
  const onRemoveService = (id: string) => {
    setServices((prev) => prev.filter((s) => s.id !== id));
  };
  const onUpdateService = (id: string, patch: Partial<ServiceRow>) => {
    setServices((prev) => prev.map((s) => (s.id === id ? { ...s, ...patch } : s)));
  };
  const onRedetect = async (id: string) => {
    if (!picked) return;
    const svc = services.find((s) => s.id === id);
    if (!svc) return;
    onUpdateService(id, { detecting: true });
    const { installationId, repo } = picked;
    const { owner, repo: rname } = ownerRepoFromFull(repo.fullName);
    try {
      const res: DetectRuntimeResponse = await detect.mutateAsync({
        installationId,
        owner,
        repo: rname,
        branch: repo.defaultBranch,
        path: svc.path,
      });
      onUpdateService(id, {
        detecting: false,
        runtime: res.runtime,
        port: res.port,
        reason: res.reason,
      });
    } catch {
      onUpdateService(id, { detecting: false });
    }
  };

  const onDeploy = async () => {
    if (!name) {
      toast.error("Project name required");
      return;
    }
    if (!picked) {
      toast.error("Pick a repo");
      return;
    }
    if (services.some((s) => !s.name.trim())) {
      toast.error("Every service needs a name");
      return;
    }
    setSubmitting(true);
    try {
      // 1. Create the project
      await create.mutateAsync({
        name,
        defaultRepo: {
          url: `https://github.com/${picked.repo.fullName}`,
          defaultBranch: picked.repo.defaultBranch,
        },
        github: { installationId: picked.installationId },
        previews: { enabled: true, ttlDays: 7 },
      });

      // 2. Add each service
      for (const svc of services) {
        try {
          await api(`/api/projects/${encodeURIComponent(name)}/services`, {
            method: "POST",
            body: {
              name: svc.name,
              repo: svc.path ? { url: `https://github.com/${picked.repo.fullName}`, path: svc.path } : undefined,
              runtime: svc.runtime,
              port: svc.port,
            },
          });
        } catch (e) {
          if (e instanceof ApiError) {
            toast.error(`Service ${svc.name}: ${e.message}`);
          }
          throw e;
        }
      }

      // 3. Add chosen addons
      for (const a of addons.filter((x) => x.enabled)) {
        try {
          await api(`/api/projects/${encodeURIComponent(name)}/addons`, {
            method: "POST",
            body: { name: a.kind, kind: a.kind },
          });
        } catch (e) {
          if (e instanceof ApiError) {
            toast.error(`Addon ${a.kind}: ${e.message}`);
          }
        }
      }

      toast.success("Project deployed");
      router.replace(`/projects/${encodeURIComponent(name)}`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to create project");
    } finally {
      setSubmitting(false);
    }
  };

  // Render branches: GitHub App not configured → message. Configured but
  // no installations → install CTA. Has installations → repo picker +
  // services + addons + deploy.
  return (
    <div className="mx-auto max-w-3xl p-6 lg:p-8 space-y-6">
      <div>
        <h1 className="font-heading text-2xl font-semibold tracking-tight">New project</h1>
        <p className="mt-1 text-sm text-[var(--text-secondary)]">
          Pick a repo. kuso detects services, suggests addons, and deploys on click.
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Project name</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="space-y-1.5">
            <Label htmlFor="name">Name</Label>
            <Input
              id="name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="my-product"
              className="font-mono"
            />
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Repository</CardTitle>
        </CardHeader>
        <CardContent>
          {installURL.isPending || installs.isPending ? (
            <Skeleton className="h-24 w-full" />
          ) : !installURL.data?.configured ? (
            <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4 text-sm text-[var(--text-secondary)]">
              GitHub App not configured on this kuso instance. Set
              <span className="font-mono"> GITHUB_APP_ID</span> +
              <span className="font-mono"> GITHUB_APP_PRIVATE_KEY</span> in the server env.
            </div>
          ) : (installs.data ?? []).length === 0 ? (
            <div className="space-y-2">
              <p className="text-sm text-[var(--text-secondary)]">
                No GitHub installations yet. Install the kuso GitHub App to grant repo access.
              </p>
              <a
                href={installURL.data.url}
                target="_blank"
                rel="noreferrer"
                className="inline-flex h-9 items-center gap-1.5 rounded-sm border border-[var(--border-default)] bg-[var(--bg-tertiary)] px-5 text-sm font-medium hover:bg-[var(--accent-subtle)]"
              >
                <Github className="h-4 w-4" />
                Install kuso GitHub App
              </a>
            </div>
          ) : (
            <div className="space-y-3">
              {!picked && (
                <>
                  <Input
                    type="search"
                    value={repoQuery}
                    onChange={(e) => setRepoQuery(e.target.value)}
                    placeholder={`Filter ${allRepos.length} repositories…`}
                    className="font-mono"
                    autoFocus
                  />
                  <ul className="max-h-72 overflow-auto divide-y divide-[var(--border-subtle)] rounded-md border border-[var(--border-subtle)]">
                    {filteredRepos.length === 0 ? (
                      <li className="px-3 py-2 text-sm text-[var(--text-tertiary)]">
                        No repos match {repoQuery ? `"${repoQuery}"` : "this filter"}.
                      </li>
                    ) : (
                      filteredRepos.map(({ installationId, repo }) => (
                        <li key={`${installationId}/${repo.fullName}`}>
                          <button
                            type="button"
                            onClick={() => setPicked({ installationId, repo })}
                            className="flex w-full items-center justify-between gap-3 px-3 py-2 text-left text-sm hover:bg-[var(--bg-tertiary)]"
                          >
                            <span className="flex items-center gap-2 truncate">
                              <Github className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
                              <span className="font-mono truncate">{repo.fullName}</span>
                              {repo.private && (
                                <span className="rounded bg-[var(--bg-tertiary)] px-1.5 py-0.5 font-mono text-[10px] text-[var(--text-tertiary)]">
                                  private
                                </span>
                              )}
                            </span>
                            <ArrowRight className="h-3.5 w-3.5 text-[var(--text-tertiary)] shrink-0" />
                          </button>
                        </li>
                      ))
                    )}
                  </ul>
                </>
              )}
              {picked && (
                <div className="flex items-center justify-between rounded-md border border-[var(--accent)]/40 bg-[var(--accent-subtle)] px-3 py-2 text-sm">
                  <span className="flex items-center gap-2 truncate">
                    <Check className="h-4 w-4 text-[var(--accent)]" />
                    <span className="font-mono truncate">{picked.repo.fullName}</span>
                    <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
                      {picked.repo.defaultBranch}
                    </span>
                  </span>
                  <button
                    type="button"
                    onClick={() => setPicked(null)}
                    className="text-xs text-[var(--text-secondary)] underline"
                  >
                    change
                  </button>
                </div>
              )}
            </div>
          )}
        </CardContent>
      </Card>

      {picked && (
        <>
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center justify-between">
                <span>Detected services</span>
                <Button variant="outline" size="sm" type="button" onClick={onAddService}>
                  <Plus className="h-3.5 w-3.5" /> Add
                </Button>
              </CardTitle>
            </CardHeader>
            <CardContent>
              <ul className="space-y-2">
                {services.map((s) => (
                  <li
                    key={s.id}
                    className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-3"
                  >
                    <div className="flex items-center gap-2">
                      <RuntimeIcon runtime={s.runtime} />
                      <Input
                        value={s.name}
                        onChange={(e) => onUpdateService(s.id, { name: e.target.value })}
                        placeholder="service name"
                        className="font-mono w-48"
                      />
                      <Input
                        value={s.path}
                        onChange={(e) => onUpdateService(s.id, { path: e.target.value })}
                        placeholder="path (root if empty)"
                        className="font-mono flex-1"
                        onBlur={() => onRedetect(s.id)}
                      />
                      <Input
                        type="number"
                        value={s.port ?? ""}
                        onChange={(e) =>
                          onUpdateService(s.id, {
                            port: e.target.value ? parseInt(e.target.value, 10) : undefined,
                          })
                        }
                        placeholder="port"
                        className="font-mono w-20"
                      />
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        type="button"
                        onClick={() => onRemoveService(s.id)}
                        aria-label="Remove service"
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    </div>
                    <div className="mt-2 font-mono text-[10px] text-[var(--text-tertiary)]">
                      {s.detecting
                        ? "detecting…"
                        : s.runtime
                          ? `${s.runtime} · ${s.reason ?? ""}`
                          : "unknown runtime"}
                    </div>
                  </li>
                ))}
              </ul>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>Suggested addons</CardTitle>
            </CardHeader>
            <CardContent>
              <p className="mb-3 text-xs text-[var(--text-tertiary)]">
                kuso scanned your repo for env-var hints and pre-checked the matches. Each
                addon's connection secret is wired into every service automatically.
              </p>
              <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
                {addons.map((a) => (
                  <label
                    key={a.kind}
                    className={cn(
                      "flex cursor-pointer items-center gap-2 rounded-md border px-3 py-2 text-sm transition-colors",
                      a.enabled
                        ? "border-[var(--accent)]/40 bg-[var(--accent-subtle)]"
                        : "border-[var(--border-subtle)] bg-[var(--bg-secondary)] hover:bg-[var(--bg-tertiary)]"
                    )}
                  >
                    <input
                      type="checkbox"
                      checked={a.enabled}
                      onChange={(e) =>
                        setAddons((prev) =>
                          prev.map((x) =>
                            x.kind === a.kind ? { ...x, enabled: e.target.checked } : x
                          )
                        )
                      }
                      className="h-4 w-4"
                    />
                    <AddonIcon kind={a.kind} />
                    <span className="font-medium">{addonLabel(a.kind)}</span>
                    {a.reason && (
                      <span className="ml-auto truncate font-mono text-[9px] text-[var(--text-tertiary)]">
                        ✦
                      </span>
                    )}
                  </label>
                ))}
              </div>
            </CardContent>
          </Card>

          <Separator />

          <div className="flex items-center justify-between">
            <p className="text-xs text-[var(--text-tertiary)]">
              Defaults: previews on, base domain auto, every service in production env.
            </p>
            <Button
              type="button"
              size="lg"
              onClick={onDeploy}
              disabled={submitting || !name || services.length === 0}
            >
              <Rocket className="h-4 w-4" />
              {submitting ? "Deploying…" : "Deploy"}
            </Button>
          </div>
        </>
      )}
    </div>
  );
}
