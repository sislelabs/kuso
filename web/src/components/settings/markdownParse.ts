// Minimal Markdown parser for release notes. Produces a plain AST that
// Markdown.tsx renders as React elements, so raw HTML in the input is
// never injected: it ends up as escaped text. Covers what git-cliff
// emits (headings, bullet lists, bold, code, links) plus paragraphs,
// ordered lists and fenced code.

export type Inline =
  | { type: "text"; value: string }
  | { type: "code"; value: string }
  | { type: "strong"; children: Inline[] }
  | { type: "em"; children: Inline[] }
  | { type: "link"; href: string; children: Inline[] };

export type Block =
  | { type: "heading"; level: 1 | 2 | 3 | 4 | 5 | 6; children: Inline[] }
  | { type: "paragraph"; children: Inline[] }
  | { type: "list"; ordered: boolean; items: Inline[][] }
  | { type: "code"; value: string };

export function stripHtmlComments(src: string): string {
  // An unterminated comment swallows the rest, matching how a browser
  // would hide it.
  return src.replace(/<!--[\s\S]*?(?:-->|$)/g, "");
}

// safeHref allows only absolute http(s) URLs. Everything else
// (javascript:, data:, relative paths) renders as plain text.
export function safeHref(raw: string): string | null {
  const href = raw.trim();
  try {
    const u = new URL(href);
    if (u.protocol === "http:" || u.protocol === "https:") return u.toString();
  } catch {
    // not an absolute URL
  }
  return null;
}

export function parseInline(src: string): Inline[] {
  const out: Inline[] = [];
  let buf = "";
  const flush = () => {
    if (buf) out.push({ type: "text", value: buf });
    buf = "";
  };
  let i = 0;
  while (i < src.length) {
    const ch = src[i];
    if (ch === "\\" && i + 1 < src.length && /[\\`*_[\]()#+\-.!]/.test(src[i + 1])) {
      buf += src[i + 1];
      i += 2;
      continue;
    }
    if (ch === "`") {
      const end = src.indexOf("`", i + 1);
      if (end > i) {
        flush();
        out.push({ type: "code", value: src.slice(i + 1, end) });
        i = end + 1;
        continue;
      }
    }
    if ((ch === "*" || ch === "_") && src[i + 1] === ch) {
      const marker = ch + ch;
      const end = src.indexOf(marker, i + 2);
      if (end > i + 2) {
        flush();
        out.push({ type: "strong", children: parseInline(src.slice(i + 2, end)) });
        i = end + 2;
        continue;
      }
    }
    if (ch === "*" || (ch === "_" && (i === 0 || /\W/.test(src[i - 1])))) {
      const end = src.indexOf(ch, i + 1);
      if (end > i + 1 && src[i + 1] !== " ") {
        flush();
        out.push({ type: "em", children: parseInline(src.slice(i + 1, end)) });
        i = end + 1;
        continue;
      }
    }
    if (ch === "[") {
      const close = findClosingBracket(src, i);
      if (close > i && src[close + 1] === "(") {
        const paren = findClosingParen(src, close + 1);
        if (paren > close) {
          flush();
          const label = parseInline(src.slice(i + 1, close));
          const href = safeHref(src.slice(close + 2, paren));
          if (href) out.push({ type: "link", href, children: label });
          else out.push(...label);
          i = paren + 1;
          continue;
        }
      }
    }
    buf += ch;
    i++;
  }
  flush();
  return out;
}

function findClosingBracket(src: string, open: number): number {
  let depth = 0;
  for (let j = open; j < src.length; j++) {
    if (src[j] === "[") depth++;
    else if (src[j] === "]") {
      depth--;
      if (depth === 0) return j;
    }
  }
  return -1;
}

function findClosingParen(src: string, open: number): number {
  let depth = 0;
  for (let j = open; j < src.length; j++) {
    if (src[j] === "(") depth++;
    else if (src[j] === ")") {
      depth--;
      if (depth === 0) return j;
    }
  }
  return -1;
}

const HEADING = /^(#{1,6})\s+(.*?)\s*#*\s*$/;
const BULLET = /^\s*[-*+]\s+(.*)$/;
const ORDERED = /^\s*\d+[.)]\s+(.*)$/;
const FENCE = /^\s*```/;

export function parseMarkdown(input: string): Block[] {
  const lines = stripHtmlComments(input).replace(/\r\n?/g, "\n").split("\n");
  const blocks: Block[] = [];
  let para: string[] = [];
  const flushPara = () => {
    if (para.length) blocks.push({ type: "paragraph", children: parseInline(para.join(" ")) });
    para = [];
  };

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (!line.trim()) {
      flushPara();
      continue;
    }
    if (FENCE.test(line)) {
      flushPara();
      const body: string[] = [];
      i++;
      while (i < lines.length && !FENCE.test(lines[i])) body.push(lines[i++]);
      blocks.push({ type: "code", value: body.join("\n") });
      continue;
    }
    const h = HEADING.exec(line);
    if (h) {
      flushPara();
      const level = h[1].length as 1 | 2 | 3 | 4 | 5 | 6;
      blocks.push({ type: "heading", level, children: parseInline(h[2]) });
      continue;
    }
    const bullet = BULLET.exec(line);
    const ordered = bullet ? null : ORDERED.exec(line);
    if (bullet || ordered) {
      flushPara();
      const isOrdered = !!ordered;
      const re = isOrdered ? ORDERED : BULLET;
      const items: string[] = [];
      let j = i;
      while (j < lines.length) {
        const m = re.exec(lines[j]);
        if (m) {
          items.push(m[1]);
        } else if (lines[j].trim() && /^\s{2,}\S/.test(lines[j]) && items.length) {
          // indented continuation of the previous item
          items[items.length - 1] += ` ${lines[j].trim()}`;
        } else {
          break;
        }
        j++;
      }
      blocks.push({ type: "list", ordered: isOrdered, items: items.map(parseInline) });
      i = j - 1;
      continue;
    }
    para.push(line.trim());
  }
  flushPara();
  return blocks;
}
