import type { ReactNode } from "react";
import type { MdNode } from "@/features/marketplace";

// Maps parsed inline markdown to React elements; no raw HTML path exists.
// links: false renders link text only, for use inside another control.
export function renderMarkdown(nodes: MdNode[], opts: { links?: boolean } = {}): ReactNode[] {
  const links = opts.links ?? true;
  return nodes.map((n, i) => {
    switch (n.kind) {
      case "text":
        return n.text;
      case "code":
        return (
          <code key={i} className="rounded bg-[var(--bg-tertiary)] px-1 py-px font-mono text-[0.92em] text-[var(--text-primary)]">
            {n.text}
          </code>
        );
      case "strong":
        return (
          <strong key={i} className="font-semibold text-[var(--text-primary)]">
            {renderMarkdown(n.children, opts)}
          </strong>
        );
      case "em":
        return <em key={i}>{renderMarkdown(n.children, opts)}</em>;
      case "link":
        if (!links) return <span key={i}>{renderMarkdown(n.children, opts)}</span>;
        return (
          <a
            key={i}
            href={n.href}
            target="_blank"
            rel="noopener noreferrer"
            className="text-[var(--link)] underline underline-offset-2 hover:text-[var(--text-primary)]"
          >
            {renderMarkdown(n.children, opts)}
          </a>
        );
    }
  });
}
