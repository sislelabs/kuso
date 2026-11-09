"use client";

import { useEffect, useRef } from "react";
import { useMutation } from "@tanstack/react-query";
import { recordHealthcheckTest } from "@/features/staged";
import { Loader2, Play } from "lucide-react";
import { Button } from "@/components/ui/button";
import { StatusPill } from "@/components/ui/status-pill";
import { useCanOnProject, Perms } from "@/features/auth";
import {
  testHealthcheck,
  healthcheckTestPassed,
  isHealthcheckTestUnsupported,
  type HealthcheckTestRequest,
  type HealthcheckTestResult,
} from "@/features/services";
import { describeError } from "@/lib/errors";
import type { StatusMeta } from "@/lib/status";

// Mirrors the server's guard so obvious mistakes are explained before a
// round-trip; the server stays the authority.
function pathProblem(path: string): string | null {
  if (!path) return "Enter a path to test.";
  if (!path.startsWith("/") || path.startsWith("//")) return "The path must start with a single /.";
  if (/[\s@\\]/.test(path)) return "The path can't contain spaces, @ or \\.";
  return null;
}

interface RunnerInput {
  project: string;
  service: string;
  env: string;
  path: string;
  port: string;
}

export interface HealthcheckTestRunner {
  run: () => void;
  pending: boolean;
  disabledReason: string | null;
  // Only the result for the path + port currently in the form; an edit
  // hides the old answer instead of leaving it next to a new value.
  result: HealthcheckTestResult | undefined;
  error: unknown;
}

export function useHealthcheckTestRunner({ project, service, env, path, port }: RunnerInput): HealthcheckTestRunner {
  const lastRecorded = useRef<HealthcheckTestResult | undefined>(undefined);
  const canWrite = useCanOnProject(project, Perms.ServicesWrite);
  const trimmed = path.trim();
  const portNum = Number(port);
  const body: HealthcheckTestRequest = {
    path: trimmed,
    env,
    ...(Number.isInteger(portNum) && portNum > 0 ? { port: portNum } : {}),
  };
  const m = useMutation({
    mutationFn: (b: HealthcheckTestRequest) => testHealthcheck(project, service, b),
    // The outcome, including failure, is shown inline under the field.
    meta: { skipGlobalErrorToast: true },
  });
  const current = m.variables?.path === body.path && m.variables?.port === body.port;
  // Attach each new result to the staged health check so the deploy
  // review can show it (and ask before shipping a path that failed).
  useEffect(() => {
    const r = m.data;
    if (!r || r === lastRecorded.current || !m.variables) return;
    lastRecorded.current = r;
    recordHealthcheckTest(project, service, {
      path: m.variables.path,
      port: m.variables.port ? String(m.variables.port) : "",
      passed: healthcheckTestPassed(r),
      summary: healthcheckTestSummary(r),
      at: Date.now(),
    });
  }, [m.data, m.variables, project, service]);
  const disabledReason = !canWrite ? "Only project editors can run a test." : pathProblem(trimmed);
  return {
    run: () => m.mutate(body),
    pending: m.isPending,
    disabledReason,
    result: current ? m.data : undefined,
    error: current ? m.error : null,
  };
}

export function HealthcheckTestButton({ runner }: { runner: HealthcheckTestRunner }) {
  const disabled = runner.pending || runner.disabledReason !== null;
  return (
    <Button
      type="button"
      variant="secondary"
      size="sm"
      onClick={runner.run}
      disabled={disabled}
      title={runner.disabledReason ?? "Send one GET to a running pod now, using the values in this form"}
      aria-describedby={runner.disabledReason ? "hc-test-reason" : undefined}
    >
      {runner.pending ? <Loader2 className="motion-safe:animate-spin" aria-hidden /> : <Play aria-hidden />}
      Test path
    </Button>
  );
}

// healthcheckTestSummary is the one-line outcome the deploy review shows.
export function healthcheckTestSummary(r: HealthcheckTestResult): string {
  if (r.error || r.status === undefined) return `No answer${r.error ? `: ${r.error}` : ""}`;
  return `${r.status}${r.statusText ? ` ${r.statusText}` : ""}`;
}

function resultStatus(r: HealthcheckTestResult): StatusMeta {
  if (r.error || r.status === undefined) return { label: "No answer", tone: "danger" };
  const label = `${r.status}${r.statusText ? ` ${r.statusText}` : ""}`;
  return { label, tone: healthcheckTestPassed(r) ? "success" : "danger" };
}

export function HealthcheckTestResultView({ runner }: { runner: HealthcheckTestRunner }) {
  const { result, error, disabledReason, pending } = runner;
  if (disabledReason && !pending && !result) {
    return (
      <p id="hc-test-reason" className="-mt-2 text-xs text-[var(--text-secondary)]">
        {disabledReason}
      </p>
    );
  }
  if (error) {
    const msg = isHealthcheckTestUnsupported(error)
      ? "Testing isn't available on this server yet. It arrives with a kuso update."
      : // The server prefixes its sentinel ("projects: invalid: …").
        describeError(error, "The test couldn't run.").replace(/^projects: [a-z ]+: /i, () => "").replace(/^./, (c) => c.toUpperCase());
    return (
      <p role="status" className="-mt-2 text-xs text-[var(--text-secondary)]">
        {msg}
      </p>
    );
  }
  if (!result) return null;
  const passed = healthcheckTestPassed(result);
  const redirect = result.status !== undefined && result.status >= 300 && result.status < 400;
  return (
    <div
      role="status"
      className="-mt-2 space-y-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-3 text-xs"
    >
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <StatusPill status={resultStatus(result)} variant="tinted" />
        <span className="text-[var(--text-secondary)]">
          {result.latencyMs} ms from pod <span className="font-mono text-[var(--text-primary)]">{result.pod}</span>
        </span>
      </div>
      <p className="text-[var(--text-secondary)]">
        {result.error
          ? `The pod didn't answer: ${result.error}.`
          : redirect
            ? `Redirects count as healthy. kuso didn't follow it${result.location ? "; it points to" : "."}`
            : passed
              ? "Healthy: pods answering like this are marked ready."
              : "Unhealthy: pods answering like this would be taken out of traffic and restarted."}
        {redirect && result.location && (
          <>
            {" "}
            <span className="break-all font-mono text-[var(--text-primary)]">{result.location}</span>.
          </>
        )}
      </p>
      {result.bodySnippet && (
        <pre className="max-h-32 overflow-auto whitespace-pre-wrap break-all rounded bg-[var(--bg-tertiary)] p-2 font-mono text-[12px] text-[var(--text-primary)]">
          {result.bodySnippet}
        </pre>
      )}
    </div>
  );
}
