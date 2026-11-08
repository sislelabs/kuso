import type { MarketplaceApp } from "./api";

// Inline-markdown subset used in catalog descriptions. Parsed into a
// token tree that the UI maps to React elements, so template text can
// never inject markup.
export type MdNode =
  | { kind: "text"; text: string }
  | { kind: "code"; text: string }
  | { kind: "strong"; children: MdNode[] }
  | { kind: "em"; children: MdNode[] }
  | { kind: "link"; href: string; children: MdNode[] };

// Order matters: code first so markers inside backticks stay literal.
// `_` emphasis needs non-word chars around it so snake_case survives.
const INLINE_RE =
  /`([^`]+)`|\*\*(?=\S)(.+?)(?<=\S)\*\*|\[([^\]]+)\]\(([^)\s]+)\)|(?<![\w*])\*(?=[^\s*])(.+?)(?<=[^\s*])\*(?![\w*])|(?<![\w_])_(?=[^\s_])(.+?)(?<=[^\s_])_(?![\w_])/g;

export function safeHref(raw: string): string | null {
  if (!/^https?:\/\//i.test(raw)) return null;
  try {
    const u = new URL(raw);
    return u.protocol === "http:" || u.protocol === "https:" ? u.href : null;
  } catch {
    return null;
  }
}

export function parseInlineMarkdown(src: string): MdNode[] {
  const out: MdNode[] = [];
  const pushText = (text: string) => {
    if (!text) return;
    const last = out[out.length - 1];
    if (last?.kind === "text") last.text += text;
    else out.push({ kind: "text", text });
  };
  const re = new RegExp(INLINE_RE.source, "g");
  let at = 0;
  for (let m = re.exec(src); m !== null; m = re.exec(src)) {
    pushText(src.slice(at, m.index));
    at = m.index + m[0].length;
    const [, code, strong, linkText, linkHref, starEm, underEm] = m;
    if (code !== undefined) out.push({ kind: "code", text: code });
    else if (strong !== undefined) out.push({ kind: "strong", children: parseInlineMarkdown(strong) });
    else if (linkText !== undefined && linkHref !== undefined) {
      const href = safeHref(linkHref);
      const children = parseInlineMarkdown(linkText);
      if (href) out.push({ kind: "link", href, children });
      else {
        for (const c of children) {
          if (c.kind === "text") pushText(c.text);
          else out.push(c);
        }
      }
    } else {
      out.push({ kind: "em", children: parseInlineMarkdown(starEm ?? underEm ?? "") });
    }
  }
  pushText(src.slice(at));
  return out;
}

export function markdownToPlain(nodes: MdNode[]): string {
  return nodes.map((n) => (n.kind === "text" || n.kind === "code" ? n.text : markdownToPlain(n.children))).join("");
}

// Cut text at the last whitespace that keeps it within max characters.
function cutAtWord(text: string, max: number): string {
  if (text.length <= max) return text;
  const cut = text.slice(0, max + 1).search(/\s\S*$/);
  return cut > 0 ? text.slice(0, cut).trimEnd() : "";
}

// truncateMarkdown keeps formatting and ends on a word boundary. Code
// spans are never split: a command cut in half reads as a real one.
export function truncateMarkdown(nodes: MdNode[], max: number): { nodes: MdNode[]; truncated: boolean } {
  const total = markdownToPlain(nodes).replace(/\s+/g, " ").trim().length;
  if (total <= max) return { nodes, truncated: false };
  let budget = max;
  const walk = (list: MdNode[]): MdNode[] => {
    const kept: MdNode[] = [];
    for (const n of list) {
      if (budget <= 0) break;
      if (n.kind === "text") {
        const text = n.text.replace(/\s+/g, " ");
        if (text.length <= budget) {
          kept.push({ kind: "text", text });
          budget -= text.length;
        } else {
          const cut = cutAtWord(text, budget);
          if (cut) kept.push({ kind: "text", text: cut });
          budget = 0;
        }
      } else if (n.kind === "code") {
        if (n.text.length > budget) {
          budget = 0;
          break;
        }
        kept.push(n);
        budget -= n.text.length;
      } else {
        const children = walk(n.children);
        if (children.length > 0) kept.push({ ...n, children });
      }
    }
    return kept;
  };
  const kept = walk(nodes);
  // Drop trailing whitespace/punctuation so the ellipsis sits on a word.
  const last = kept[kept.length - 1];
  if (last?.kind === "text") {
    last.text = last.text.replace(/[\s,;:.(—-]+$/, "");
    if (!last.text) kept.pop();
  }
  return { nodes: kept, truncated: true };
}

export interface VersionDisplay {
  label: string;
  // Full ref for a tooltip when the label hides it.
  detail?: string;
  // Literal versions render in mono; the "latest build" phrase doesn't.
  literal: boolean;
}

const DIGEST_RE = /^(?:.*@)?sha256:[0-9a-f]{12,64}$/i;
// A commit-ish: 7–40 hex chars containing at least one letter (so a
// date-like 20241022 isn't mistaken for one), optionally "sha-" prefixed.
const COMMIT_RE = /^(?:sha-)?(?=[0-9a-f]*[a-f])[0-9a-f]{7,40}$/i;

export function versionDisplay(version: string | undefined): VersionDisplay | null {
  const v = version?.trim();
  if (!v) return null;
  if (DIGEST_RE.test(v)) return { label: "latest build", detail: `Pinned to image digest ${v}`, literal: false };
  if (COMMIT_RE.test(v)) return { label: "latest build", detail: `Built from commit ${v.replace(/^sha-/i, "")}`, literal: false };
  return { label: /^\d+\.\d+/.test(v) ? `v${v}` : v, literal: true };
}

// Lettermark tints, all theme tokens so they track light/dark.
export const LETTERMARK_TINTS = ["--chart-1", "--chart-2", "--chart-3", "--chart-4", "--chart-5"] as const;

export function lettermarkTint(app: string): (typeof LETTERMARK_TINTS)[number] {
  // FNV-1a: stable across sessions and builds.
  let h = 0x811c9dc5;
  for (let i = 0; i < app.length; i++) {
    h ^= app.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return LETTERMARK_TINTS[(h >>> 0) % LETTERMARK_TINTS.length];
}

export function lettermark(title: string): string {
  const ch = title.trim().match(/[\p{L}\p{N}]/u)?.[0];
  return ch ? ch.toUpperCase() : "?";
}

// Servers before the firstRunSetup/defaultCredentials manifest fields
// don't send them. This fallback can go once every instance runs a
// release that ships the flags.
const SETUP_RISK_FALLBACK: Record<string, Pick<MarketplaceApp, "firstRunSetup" | "defaultCredentials">> = {
  umami: { defaultCredentials: "admin/umami" },
  n8n: { firstRunSetup: true },
  metabase: { firstRunSetup: true },
  "uptime-kuma": { firstRunSetup: true },
  plausible: { firstRunSetup: true },
};

export interface SetupRisk {
  firstRunSetup: boolean;
  defaultCredentials?: string;
}

export function setupRisk(app: MarketplaceApp): SetupRisk | null {
  const sent = app.firstRunSetup !== undefined || app.defaultCredentials !== undefined;
  const src = sent ? app : (SETUP_RISK_FALLBACK[app.name] ?? {});
  const risk: SetupRisk = { firstRunSetup: src.firstRunSetup === true, defaultCredentials: src.defaultCredentials || undefined };
  return risk.firstRunSetup || risk.defaultCredentials ? risk : null;
}
