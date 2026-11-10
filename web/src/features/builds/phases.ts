// parseBuildLog folds a build pod's plain-text log into steps the UI can
// show as a foldable list: the clone/prepare preamble, one step per
// BuildKit vertex (`#N [stage x/y] CMD`, `#N DONE 1.2s`, `#N CACHED`,
// `#N ERROR: …`), buildpacks lifecycle phases (`===> BUILDING`), and the
// error trailer BuildKit prints after a failed solve.

import { isPlatformFailure } from "./platform";

export type StepStatus = "done" | "cached" | "error" | "canceled" | "running" | "unknown";
export type StepKind = "prepare" | "buildkit" | "lifecycle" | "summary";

export interface StepLine {
  // Index into the raw line array (stable key, and "line N" for search).
  idx: number;
  text: string;
  // Seconds since the step started, when BuildKit stamped the line.
  t?: number;
  // How many identical consecutive lines this row stands for.
  repeat: number;
  error: boolean;
}

export interface BuildStep {
  id: string;
  kind: StepKind;
  title: string;
  // Secondary label, e.g. "prod-deps · 5/5" or "internal".
  detail?: string;
  status: StepStatus;
  durationS?: number;
  lines: StepLine[];
  // Raw lines the step owned before noise was folded away.
  rawCount: number;
  errorMessage?: string;
  // Bookkeeping steps (cache hits, metadata loads) that the viewer folds
  // into one summary row.
  minor: boolean;
}

export interface ParsedBuildLog {
  steps: BuildStep[];
  // False when the log has no BuildKit/lifecycle markers; the viewer
  // then shows it raw since there's nothing to fold.
  structured: boolean;
  // The archived tail started inside a step, so earlier steps are gone.
  truncated: boolean;
  failedStepId?: string;
}

const BK = /^#(\d+) (.*)$/;
const BK_DONE = /^DONE (\d+(?:\.\d+)?)s$/;
const BK_STAMPED = /^(\d+\.\d+)(?: (.*))?$/;
const BK_ERROR = /^ERROR:?\s*(.*)$/;
const BK_HEADER = /^\[([^\]]+)\]\s*(.*)$/;
const LIFECYCLE = /^===> ([A-Z][A-Z -]+)$/;
const CONTAINER = /^--- container: (.+) ---$/;

const ERROR_RE =
  /\b(error|err!|fatal|panic|failed|failure|exception|traceback|cannot|could not|denied|exit code|exited with|oomkilled|killed)\b|^\s*>>>|\s>>>\s|^Dockerfile:\d+/i;
const NOT_ERROR_RE = /\bnpm (notice|warn)\b|\bdeprecated\b|\b0 errors?\b|\bwarn(ing)?\b/i;

// The clone step has one name whether it comes from the log's preamble,
// the archived container section or the server's failure classification.
export const CLONE_STEP = "Clone repository";

const CONTAINER_TITLES: Record<string, string> = {
  clone: CLONE_STEP,
  "cache-init": "Prepare build cache",
  buildkit: "Connect to BuildKit",
  build: "Run the build",
  "env-detect": "Detect environment variables",
  "nixpacks-plan": "Plan nixpacks build",
  nixpacks: "Generate Dockerfile with nixpacks",
  static: "Build static site",
};

interface Draft {
  step: BuildStep;
  raw: { idx: number; text: string; t?: number }[];
  headerSeen: boolean;
  terminated: boolean;
}

export function isErrorLine(text: string): boolean {
  return ERROR_RE.test(text) && !NOT_ERROR_RE.test(text);
}

export function parseBuildLog(lines: string[], opts: { live?: boolean; failed?: boolean } = {}): ParsedBuildLog {
  const drafts: Draft[] = [];
  const byVertex = new Map<number, Draft>();
  let prepare: Draft | null = null;
  let lifecycle: Draft | null = null;
  let trailer: Draft | null = null;
  let sawBuildkit = false;
  let truncated = false;
  let firstContent = true;

  const open = (kind: StepKind, id: string, title: string, detail?: string): Draft => {
    const d: Draft = {
      step: { id, kind, title, detail, status: "unknown", lines: [], rawCount: 0, minor: false },
      raw: [],
      headerSeen: true,
      terminated: false,
    };
    drafts.push(d);
    return d;
  };

  lines.forEach((rawLine, idx) => {
    const line = lastFrame(rawLine);
    const bk = BK.exec(line);
    if (bk) {
      sawBuildkit = true;
      prepare = null;
      lifecycle = null;
      const n = Number(bk[1]);
      const rest = bk[2].trimEnd();
      let d = byVertex.get(n);
      const stamped = BK_STAMPED.exec(rest);
      if (!d) {
        const headerless = !!stamped || BK_DONE.test(rest) || rest === "CACHED" || BK_ERROR.test(rest);
        if (firstContent && headerless) truncated = true;
        d = open("buildkit", `bk-${n}`, headerless ? `Step #${n}` : rest);
        d.headerSeen = !headerless;
        byVertex.set(n, d);
        firstContent = false;
        if (!headerless) return;
      }
      firstContent = false;
      if (rest === "..." || rest === d.step.title) return;
      const done = BK_DONE.exec(rest);
      if (done) {
        d.step.status = "done";
        d.step.durationS = Number(done[1]);
        d.terminated = true;
        return;
      }
      if (rest === "CACHED") {
        d.step.status = "cached";
        d.terminated = true;
        return;
      }
      if (rest === "CANCELED") {
        d.step.status = "canceled";
        d.terminated = true;
        return;
      }
      const err = BK_ERROR.exec(rest);
      if (err) {
        d.step.status = "error";
        d.step.errorMessage = err[1];
        d.terminated = true;
        return;
      }
      if (stamped) {
        d.raw.push({ idx, text: stamped[2] ?? "", t: Number(stamped[1]) });
      } else {
        d.raw.push({ idx, text: rest });
      }
      return;
    }

    const container = CONTAINER.exec(line.trim());
    if (container) {
      const name = container[1].trim();
      prepare = open("prepare", `ctr-${drafts.length}`, CONTAINER_TITLES[name] ?? `Run ${name}`);
      lifecycle = null;
      firstContent = false;
      return;
    }

    const lc = LIFECYCLE.exec(line.trim());
    if (lc) {
      const word = lc[1].trim().toLowerCase();
      lifecycle = open("lifecycle", `lc-${drafts.length}`, word.charAt(0).toUpperCase() + word.slice(1));
      prepare = null;
      firstContent = false;
      return;
    }

    if (lifecycle) {
      lifecycle.raw.push({ idx, text: line });
      return;
    }

    if (sawBuildkit) {
      if (!trailer && line.trim() === "") return;
      if (!trailer) trailer = open("summary", "summary", "Build output");
      trailer.raw.push({ idx, text: line });
      return;
    }

    if (line.trim() === "" && !prepare) return;
    if (!prepare) prepare = open("prepare", `pre-${drafts.length}`, CLONE_STEP);
    prepare.raw.push({ idx, text: line });
    firstContent = false;
  });

  const structured = sawBuildkit || drafts.some((d) => d.step.kind === "lifecycle");
  const anyError = drafts.some((d) => d.step.status === "error");

  // Close out steps that never printed a terminator.
  const last = drafts[drafts.length - 1];
  for (const d of drafts) {
    const s = d.step;
    if (d.terminated) continue;
    const isLast = d === last;
    if (s.kind === "summary") {
      const errorish = d.raw.some((r) => /^error: |^Dockerfile:\d+|^ERROR:/.test(r.text.trim()));
      s.status = errorish || (opts.failed && !opts.live) ? "error" : "done";
      if (s.status === "error") s.title = "Error details";
      continue;
    }
    if (s.kind === "prepare" || s.kind === "lifecycle") {
      if (!isLast) s.status = "done";
      else if (opts.live) s.status = "running";
      else if (opts.failed) s.status = anyError ? "done" : "error";
      else s.status = "done";
      continue;
    }
    // A BuildKit vertex without DONE: still running while the build is,
    // otherwise its terminator was cut by the tail or interleaving.
    if (opts.live) s.status = "running";
    else if (opts.failed && !anyError && isLastVertex(drafts, d)) s.status = "error";
  }

  for (const d of drafts) {
    finishStep(d);
  }

  const ordered = [...drafts.filter((d) => d !== trailer), ...(trailer ? [trailer] : [])];
  const steps = ordered.map((d) => d.step).filter((s) => s.kind === "buildkit" || s.lines.length > 0 || s.status === "error");
  const failed =
    steps.find((s) => s.kind === "buildkit" && s.status === "error") ??
    steps.find((s) => s.status === "error");
  return { steps, structured, truncated, failedStepId: failed?.id };
}

// lastFrame keeps what a terminal would show for a line redrawn with
// carriage returns (apt and npm progress): the line prefix plus the final
// frame.
function lastFrame(raw: string): string {
  if (!raw.includes("\r")) return raw;
  const frames = raw.split("\r").filter((f) => f !== "");
  if (frames.length <= 1) return frames[0] ?? "";
  const prefix = /^#\d+ (?:\d+\.\d+ )?/.exec(frames[0])?.[0] ?? "";
  const tail = frames[frames.length - 1];
  return tail.startsWith(prefix) ? tail : prefix + tail;
}

function isLastVertex(drafts: Draft[], d: Draft): boolean {
  for (let i = drafts.length - 1; i >= 0; i--) {
    if (drafts[i].step.kind === "buildkit") return drafts[i] === d;
  }
  return false;
}

function finishStep(d: Draft) {
  const s = d.step;
  s.rawCount = d.raw.length;
  if (s.kind === "buildkit") {
    const label = buildkitLabel(s.title);
    s.title = label.title;
    s.detail = label.detail;
  }
  if (s.durationS === undefined) {
    const stamps = d.raw.filter((r) => r.t !== undefined);
    if (stamps.length > 0) s.durationS = stamps[stamps.length - 1].t;
  }
  s.lines = foldNoise(d.raw, s.status === "error" || s.kind === "summary");
  s.minor =
    s.status !== "error" &&
    s.status !== "running" &&
    (s.status === "cached" ||
      s.detail === "internal" ||
      /^(Import build cache|Resolve image config|Load metadata)/.test(s.title) ||
      (/^FROM /.test(s.title) && (s.durationS ?? 0) < 1)) &&
    s.lines.length <= 3;
}

// foldNoise drops blank lines, progress lines that a later "… done" line
// repeats, and collapses runs of identical lines into one row with a count.
function foldNoise(raw: { idx: number; text: string; t?: number }[], markErrors: boolean): StepLine[] {
  const out: StepLine[] = [];
  for (let i = 0; i < raw.length; i++) {
    const r = raw[i];
    const text = r.text.trimEnd();
    if (text.trim() === "") continue;
    if (r.t === undefined) {
      const next = raw[i + 1];
      if (next && next.t === undefined && next.text.startsWith(text + " ")) continue;
    }
    const prev = out[out.length - 1];
    if (prev && prev.text === text) {
      prev.repeat++;
      continue;
    }
    out.push({ idx: r.idx, text, t: r.t, repeat: 1, error: markErrors && isErrorLine(text) });
  }
  return out;
}

function buildkitLabel(header: string): { title: string; detail?: string } {
  const h = BK_HEADER.exec(header);
  if (h) {
    const tag = h[1].trim();
    const body = h[2].replace(/\s+/g, " ").trim();
    if (tag === "internal") return { title: sentence(body), detail: "internal" };
    const m = /^(.*?)\s+(\d+\/\d+)$/.exec(tag);
    return { title: body, detail: m ? `${m[1]} · ${m[2]}` : tag };
  }
  const t = header.replace(/\s+/g, " ").trim();
  if (/^exporting to image/i.test(t)) return { title: "Export and push image" };
  if (/^exporting cache/i.test(t)) return { title: "Export build cache", detail: "optional" };
  if (/^importing cache manifest/i.test(t)) return { title: "Import build cache" };
  if (/^resolve image config/i.test(t)) return { title: "Resolve image config", detail: t.replace(/^resolve image config for\s*/i, "") };
  return { title: sentence(t) };
}

function sentence(s: string): string {
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : s;
}

const LEAD_NOISE = /^(npm (error|err!)|error:?|err:?|fatal:?|\[error\]|e:)\s*/i;
const GENERIC = /did not complete successfully|^code \S+$|^exit (code|status)|^process ".*"/i;

// firstErrorLine is the one line worth showing in a failed build's
// history row: the first descriptive error in the failing step, else
// BuildKit's own error message.
export function firstErrorLine(parsed: ParsedBuildLog): string | undefined {
  const step = parsed.steps.find((s) => s.id === parsed.failedStepId);
  if (!step) return undefined;
  const candidates = step.lines.filter((l) => l.error).map((l) => l.text.trim());
  for (const c of candidates) {
    const rest = c.replace(LEAD_NOISE, "").trim();
    if (rest.length >= 16 && !GENERIC.test(rest)) return rest;
  }
  if (step.errorMessage) return step.errorMessage;
  const summary = parsed.steps.find((s) => s.kind === "summary");
  const solve = summary?.lines.find((l) => /^error: /i.test(l.text.trim()));
  return solve?.text.trim() ?? candidates[0];
}

// failedStepTitle names the step a failed build died in, from its log:
// "Clone repository", "RUN npm ci · build · 4/7".
export function failedStepTitle(parsed: ParsedBuildLog): string | undefined {
  const step = parsed.steps.find((s) => s.id === parsed.failedStepId);
  if (!step || step.kind === "summary") return undefined;
  return step.detail && step.detail !== "internal" ? `${step.title} · ${step.detail}` : step.title;
}

// The server names the container that exited ("… (clone)") and classifies
// some failures; either names the step without fetching the log.
const KIND_STEPS: Record<string, string> = {
  clone_ref_missing: CLONE_STEP,
  clone_unreachable: CLONE_STEP,
  dockerfile_not_found: "Run the build",
  dockerfile_missing_copy: "Run the build",
  lockfile_drift: "Install dependencies",
  dependency_resolution: "Install dependencies",
  build_command_failed: "Run the build",
  build_oom: "Run the build",
  missing_build_arg: "Run the build",
};

export function failedStepFromBuild(b: { errorMessage?: string; failureClass?: { kind?: string } }): string | undefined {
  const ctr = /\(([a-z0-9-]+)\)\s*$/i.exec(b.errorMessage ?? "")?.[1];
  // The buildctl client container is where the whole solve runs.
  if (ctr) return ctr === "buildkit" ? "Run the build" : (CONTAINER_TITLES[ctr] ?? `Run ${ctr}`);
  return KIND_STEPS[b.failureClass?.kind ?? ""];
}

// failedStep names the step a failed build died in. The log's guess is
// the more specific one inside the BuildKit solve, but it only sees steps
// that printed: it blames the last one that did (usually the clone) when
// a later init container never ran. So a non-BuildKit container the
// server names wins, and a platform failure never takes the log's guess.
export function failedStep(
  b: { errorMessage?: string; failureClass?: { kind?: string } },
  logStep?: string,
): string | undefined {
  const server = failedStepFromBuild(b);
  if (isPlatformFailure(b)) return server;
  const ctr = /\(([a-z0-9-]+)\)\s*$/i.exec(b.errorMessage ?? "")?.[1];
  if (ctr && ctr !== "buildkit") return server;
  return logStep ?? server;
}

export type DeployStage = "clone" | "build" | "push";

// stallStage is the first-deploy step a stalled build is stuck in, from
// the container the server says is waiting.
export function stallStage(container: string | undefined): DeployStage | undefined {
  if (!container) return undefined;
  return container === "clone" || container === "cache-init" ? "clone" : "build";
}

// deployStage says how far a running build has got, for the first-deploy
// progress view.
export function deployStage(parsed: ParsedBuildLog): DeployStage {
  const pushing = parsed.steps.some(
    (s) => s.title === "Export and push image" || (s.kind === "lifecycle" && /^Export/.test(s.title)),
  );
  if (pushing) return "push";
  if (parsed.steps.some((s) => s.kind === "buildkit" || s.kind === "lifecycle")) return "build";
  return "clone";
}

// formatStepDuration: "0.4s", "12s", "1m 05s".
export function formatStepDuration(s: number | undefined): string {
  if (s === undefined || !Number.isFinite(s)) return "";
  if (s < 10) return `${s.toFixed(1)}s`;
  if (s < 59.5) return `${Math.round(s)}s`;
  const total = Math.round(s);
  return `${Math.floor(total / 60)}m ${String(total % 60).padStart(2, "0")}s`;
}
