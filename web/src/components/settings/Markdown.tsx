import { Fragment, type ReactNode } from "react";
import { cn } from "@/lib/utils";
import { parseMarkdown, type Block, type Inline } from "./markdownParse";

// Markdown renders trusted-shape, untrusted-content Markdown (release
// notes) as React elements. No dangerouslySetInnerHTML anywhere.
export function Markdown({ source, className }: { source: string; className?: string }) {
  const blocks = parseMarkdown(source);
  return <div className={cn("space-y-3 text-sm leading-relaxed text-[var(--text-secondary)]", className)}>{blocks.map(renderBlock)}</div>;
}

function renderBlock(b: Block, key: number): ReactNode {
  switch (b.type) {
    case "heading": {
      // Release notes nest under a page h1 + card h2, so shift levels
      // down and keep the visual scale compact.
      const cls =
        b.level <= 2
          ? "text-base font-semibold text-[var(--text-primary)]"
          : "pt-1 text-sm font-semibold text-[var(--text-primary)]";
      const Tag = (b.level <= 2 ? "h3" : "h4") as "h3" | "h4";
      return (
        <Tag key={key} className={cls}>
          {renderInlines(b.children)}
        </Tag>
      );
    }
    case "paragraph":
      return <p key={key}>{renderInlines(b.children)}</p>;
    case "list": {
      const Tag = b.ordered ? "ol" : "ul";
      return (
        <Tag key={key} className={cn("space-y-1 pl-5", b.ordered ? "list-decimal" : "list-disc")}>
          {b.items.map((it, i) => (
            <li key={i} className="pl-0.5 marker:text-[var(--text-tertiary)]">
              {renderInlines(it)}
            </li>
          ))}
        </Tag>
      );
    }
    case "code":
      return (
        <pre
          key={key}
          className="overflow-x-auto rounded-sm border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-3 font-mono text-xs text-[var(--text-primary)]"
        >
          {b.value}
        </pre>
      );
  }
}

function renderInlines(nodes: Inline[]): ReactNode {
  return nodes.map((n, i) => <Fragment key={i}>{renderInline(n)}</Fragment>);
}

function renderInline(n: Inline): ReactNode {
  switch (n.type) {
    case "text":
      return n.value;
    case "code":
      return (
        <code className="rounded-sm bg-[var(--bg-tertiary)] px-1 py-0.5 font-mono text-[0.85em] text-[var(--text-primary)]">
          {n.value}
        </code>
      );
    case "strong":
      return <strong className="font-semibold text-[var(--text-primary)]">{renderInlines(n.children)}</strong>;
    case "em":
      return <em>{renderInlines(n.children)}</em>;
    case "link":
      return (
        <a
          href={n.href}
          target="_blank"
          rel="noopener noreferrer"
          className="text-[var(--text-primary)] underline decoration-[var(--border-strong)] underline-offset-2 hover:decoration-[var(--text-primary)]"
        >
          {renderInlines(n.children)}
        </a>
      );
  }
}
