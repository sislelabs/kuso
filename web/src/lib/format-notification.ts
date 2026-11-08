// Turns server notification text (written for Slack/Discord: markdown
// backticks, Go durations, full build CR names, raw Kubernetes Job
// messages) into what a person reads in the dashboard.

export interface TextPart {
  text: string;
  code: boolean;
}

export interface NotificationTextInput {
  type?: string;
  title: string;
  body?: string | null;
  classification?: { kind?: string; summary?: string } | null;
}

export interface NotificationText {
  title: string;
  body: TextPart[];
}

// Leading status glyphs (✓ ✗ ⚠ ↩ ⊘ ◼ …): the UI shows severity itself.
const LEADING_GLYPHS = /^(?:[\s←-⇿⊕-⊡⌀-➿⬀-⯿️]|\p{Extended_Pictographic})+/u;

const K8S_JOB_MESSAGES: ReadonlyArray<[RegExp, string]> = [
  [/^Job has reached the specified backoff limit\.?$/i, "failed"],
  [/^BackoffLimitExceeded$/i, "failed"],
  [/^Job was active longer than specified deadline\.?$/i, "timed out"],
  [/^DeadlineExceeded$/i, "timed out"],
];

// Go's time.Duration String(): "5m0s", "1h30m0s", "90s", "1.5s".
const GO_DURATION = /\b(?:(\d+)h)?(?:(\d+)m)?(\d+(?:\.\d+)?)s\b/g;

export function humanizeDuration(match: string, h?: string, m?: string, sec?: string): string {
  if (!h && !m && sec === undefined) return match;
  const hours = Number(h ?? 0);
  const mins = Number(m ?? 0);
  const secs = Math.round(Number(sec ?? 0));
  const parts: string[] = [];
  if (hours) parts.push(`${hours} h`);
  if (mins) parts.push(`${mins} min`);
  if (secs || parts.length === 0) parts.push(`${secs} s`);
  return parts.join(" ");
}

// "shop-api-c850e9b0ef35-mw883yzn" (build CR) or "c850e9b0ef35-mw883yzn"
// (image tag) → the short commit "c850e9b".
const BUILD_REF = /^(?:[a-z0-9][a-z0-9-]*-)?([0-9a-f]{12,40})-[a-z0-9]{6,10}$/;

function shortenCode(code: string): string {
  const m = code.match(BUILD_REF);
  return m ? m[1].slice(0, 7) : code;
}

function splitCode(text: string): TextPart[] {
  const out: TextPart[] = [];
  const re = /`([^`]+)`/g;
  let last = 0;
  for (let m = re.exec(text); m; m = re.exec(text)) {
    if (m.index > last) out.push({ text: text.slice(last, m.index), code: false });
    out.push({ text: shortenCode(m[1]), code: true });
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push({ text: text.slice(last), code: false });
  return out;
}

function bodyText(input: NotificationTextInput): string {
  const raw = (input.body ?? "").trim();
  const summary = input.classification?.summary?.trim();
  // A specific classification says why; "generic" only says "see logs".
  if (summary && input.classification?.kind && input.classification.kind !== "generic") return summary;
  for (const [re, what] of K8S_JOB_MESSAGES) {
    if (re.test(raw)) {
      const subject = input.type?.startsWith("build.") ? "The build" : "The job";
      return `${subject} ${what}. Open its log to see why.`;
    }
  }
  return raw || summary || "";
}

export function humanizeNotification(input: NotificationTextInput): NotificationText {
  const title = input.title.replace(LEADING_GLYPHS, "").trim();
  let text = bodyText(input).replace(GO_DURATION, humanizeDuration);
  // "back to build `x` (image `x`)": the image names the same build.
  text = text.replace(/ \(image `[^`]+`\)/g, "");
  return { title, body: splitCode(text) };
}
