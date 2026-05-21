"use client";

import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { FileCode, Play, Rocket, RotateCcw } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  applyConfig,
  getProjectSpec,
  type ConfigApplyResult,
  type ConfigPlan,
} from "@/features/projects";

// ConfigTab — config-as-code panel for a project. Renders the project's
// live state as an editable kuso.yaml document, lets the user dry-run a
// diff against the cluster, then apply it.
//
// The project view itself is canvas-only (no tab strip), so this lives
// as a section on the project settings page next to General / Previews /
// Shared Secrets — that's where every other project-level control sits.
export function ConfigTab({ project }: { project: string }) {
  // text is the editable buffer. live is the last-fetched server copy —
  // tracked so "Reset to live" and the dirty indicator have something
  // to compare against.
  const [text, setText] = useState("");
  const [live, setLive] = useState("");
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // plan from the last dry-run; result from the last apply. At most one
  // is shown at a time — running either clears the other.
  const [plan, setPlan] = useState<ConfigPlan | null>(null);
  const [result, setResult] = useState<ConfigApplyResult | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError(null);
    try {
      const yaml = await getProjectSpec(project);
      setText(yaml);
      setLive(yaml);
      setPlan(null);
      setResult(null);
    } catch (e) {
      setLoadError(e instanceof Error ? e.message : "failed to load config");
    } finally {
      setLoading(false);
    }
  }, [project]);

  useEffect(() => {
    void load();
  }, [load]);

  const onDryRun = async () => {
    setBusy(true);
    setResult(null);
    try {
      const p = await applyConfig(project, text, true);
      setPlan(p);
      toast.success("Dry run complete — review the plan below");
    } catch (e) {
      setPlan(null);
      toast.error(e instanceof Error ? e.message : "dry run failed");
    } finally {
      setBusy(false);
    }
  };

  const onApply = async () => {
    setBusy(true);
    setPlan(null);
    try {
      const res = await applyConfig(project, text, false);
      setResult(res);
      const errs = res.errors ?? [];
      if (errs.length > 0) {
        toast.error(`Applied with ${errs.length} error${errs.length > 1 ? "s" : ""}`);
      } else {
        toast.success("Config applied");
        // Re-sync the buffer with the new live state.
        void load();
      }
    } catch (e) {
      setResult(null);
      toast.error(e instanceof Error ? e.message : "apply failed");
    } finally {
      setBusy(false);
    }
  };

  const dirty = text !== live;

  return (
    <section className="space-y-4">
      <header className="flex items-center gap-2">
        <FileCode className="h-4 w-4 text-[var(--text-tertiary)]" />
        <h2 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          config as code
        </h2>
      </header>

      <div className="space-y-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 p-4">
        <p className="text-[11px] leading-relaxed text-[var(--text-secondary)]">
          The project&apos;s services, addons, and crons as a single{" "}
          <code className="rounded bg-[var(--bg-tertiary)] px-1 font-mono">kuso.yaml</code>{" "}
          document. Edit it, run a <span className="font-medium">dry run</span> to
          preview the diff, then <span className="font-medium">apply</span>. Resources
          present on the cluster but absent from the YAML are reported under{" "}
          <span className="font-mono">wouldDelete</span> — apply never deletes them
          automatically.
        </p>

        {loadError ? (
          <p className="rounded-md border border-red-500/30 bg-red-500/5 p-3 text-[12px] text-red-400">
            {loadError}
          </p>
        ) : (
          <textarea
            value={loading ? "" : text}
            onChange={(e) => setText(e.target.value)}
            disabled={loading || busy}
            spellCheck={false}
            rows={22}
            placeholder={loading ? "loading…" : ""}
            className="w-full resize-y rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-3 font-mono text-[12px] leading-relaxed text-[var(--text-secondary)] outline-none focus:border-[var(--accent)] disabled:opacity-60"
          />
        )}

        <div className="flex items-center gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={onDryRun}
            disabled={loading || busy || !!loadError}
          >
            <Play className="h-4 w-4" />
            Dry run
          </Button>
          <Button
            type="button"
            size="sm"
            onClick={onApply}
            disabled={loading || busy || !!loadError}
          >
            <Rocket className="h-4 w-4" />
            Apply
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => void load()}
            disabled={loading || busy}
          >
            <RotateCcw className="h-4 w-4" />
            Reset to live
          </Button>
          {dirty && !loading && (
            <span className="font-mono text-[10px] text-amber-400">edited</span>
          )}
        </div>
      </div>

      {plan && <PlanView plan={plan} title="Dry run — planned changes" />}
      {result && <ResultView result={result} />}
    </section>
  );
}

// ResultView renders an ApplyResult: the executed plan plus any
// per-step errors that came back.
function ResultView({ result }: { result: ConfigApplyResult }) {
  const errs = result.errors ?? [];
  return (
    <div className="space-y-3">
      <PlanView plan={result.plan} title="Apply — executed changes" />
      {errs.length > 0 && (
        <section className="rounded-md border border-red-500/30 bg-red-500/5">
          <header className="border-b border-red-500/30 px-3 py-2">
            <h3 className="font-mono text-[10px] uppercase tracking-widest text-red-400">
              {errs.length} error{errs.length > 1 ? "s" : ""}
            </h3>
          </header>
          <ul>
            {errs.map((e, i) => (
              <li
                key={`${e.resource}-${e.op}-${i}`}
                className={
                  "px-3 py-2 text-[12px]" +
                  (i < errs.length - 1 ? " border-b border-red-500/20" : "")
                }
              >
                <span className="font-mono text-[11px] text-red-300">
                  {e.op} {e.resource}
                </span>
                <span className="ml-2 text-[var(--text-secondary)]">{e.message}</span>
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}

// PlanView renders a spec.Plan as grouped create/update/delete lists.
function PlanView({ plan, title }: { plan: ConfigPlan; title: string }) {
  const groups: { label: string; tone: string; items: string[] }[] = [
    {
      label: "create",
      tone: "text-emerald-400",
      items: [
        ...plan.servicesToCreate.map((n) => `service:${n}`),
        ...plan.addonsToCreate.map((n) => `addon:${n}`),
        ...plan.cronsToCreate.map((n) => `cron:${n}`),
      ],
    },
    {
      label: "update",
      tone: "text-amber-400",
      items: [
        ...plan.servicesToUpdate.map((n) => `service:${n}`),
        ...plan.addonsToUpdate.map((n) => `addon:${n}`),
        ...plan.cronsToUpdate.map((n) => `cron:${n}`),
      ],
    },
    {
      label: "delete",
      tone: "text-red-400",
      items: [
        ...plan.servicesToDelete.map((n) => `service:${n}`),
        ...plan.addonsToDelete.map((n) => `addon:${n}`),
        ...plan.cronsToDelete.map((n) => `cron:${n}`),
      ],
    },
    {
      label: "would delete (not applied)",
      tone: "text-[var(--text-tertiary)]",
      items: plan.wouldDelete ?? [],
    },
  ];
  const empty = groups.every((g) => g.items.length === 0);
  return (
    <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <header className="border-b border-[var(--border-subtle)] px-3 py-2">
        <h3 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          {title}
        </h3>
      </header>
      {empty ? (
        <p className="px-3 py-3 font-mono text-[11px] text-[var(--text-tertiary)]">
          no changes — config matches live state
        </p>
      ) : (
        <div className="divide-y divide-[var(--border-subtle)]">
          {groups
            .filter((g) => g.items.length > 0)
            .map((g) => (
              <div key={g.label} className="px-3 py-2">
                <p className={`font-mono text-[10px] uppercase tracking-widest ${g.tone}`}>
                  {g.label} · {g.items.length}
                </p>
                <ul className="mt-1 space-y-0.5">
                  {g.items.map((it) => (
                    <li
                      key={it}
                      className="font-mono text-[11px] text-[var(--text-secondary)]"
                    >
                      {it}
                    </li>
                  ))}
                </ul>
              </div>
            ))}
        </div>
      )}
    </section>
  );
}
