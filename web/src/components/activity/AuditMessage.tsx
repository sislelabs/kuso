"use client";

import { useState } from "react";
import { useUserDirectory } from "@/features/users";
import { cn } from "@/lib/utils";
import { ExpandableText } from "./ExpandableText";
import { humanizeAuditMessage } from "./audit-text";

// AuditMessage renders an audit row's message for people, with the
// internal bits (CR names, ids, flags) behind a "details" toggle.
export function AuditMessage({
  action,
  message,
  className,
  wrap = false,
}: {
  action?: string;
  message?: string;
  className?: string;
  /** Wrap long text instead of the one-line expandable form. */
  wrap?: boolean;
}) {
  const { resolve } = useUserDirectory();
  const [open, setOpen] = useState(false);
  const { text, details } = humanizeAuditMessage(action, message, (id) => resolve(id)?.username ?? null);
  if (!text && details.length === 0) return null;
  return (
    <span className={cn("block min-w-0", className)}>
      {text &&
        (wrap ? <span className="block break-words">{text}</span> : <ExpandableText text={text} />)}
      {details.length > 0 && (
        <>
          <button
            type="button"
            aria-expanded={open}
            onClick={() => setOpen((v) => !v)}
            className="mt-0.5 text-xs text-[var(--text-tertiary)] underline-offset-2 hover:text-[var(--text-secondary)] hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
          >
            {open ? "Hide details" : "Details"}
          </button>
          {open && (
            <span className="mt-1 block break-all font-mono text-xs text-[var(--text-tertiary)]">
              {details.join(" · ")}
            </span>
          )}
        </>
      )}
    </span>
  );
}
