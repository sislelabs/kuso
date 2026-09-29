"use client";

import { useEffect, useState, type ReactNode } from "react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { ChevronDown, ClipboardPaste, Github, KeyRound, MoreHorizontal, Plus } from "lucide-react";
import type { DriftReport } from "@/features/services/api";
import { cn } from "@/lib/utils";

export interface SubscribableShape {
  subscribed: string[];
  sources: { secret: string; keys: string[] }[];
  values?: Record<string, string>;
}

export const INSTANCE_SHARED_SECRET = "kuso-instance-shared";

export function VarBadge({
  children,
  title,
  tone = "neutral",
}: {
  children: ReactNode;
  title?: string;
  tone?: "neutral" | "accent" | "warn";
}) {
  return (
    <span
      title={title}
      className={cn(
        "shrink-0 rounded border px-1 py-px font-mono text-[9px] leading-tight",
        tone === "neutral" && "border-[var(--border-subtle)] text-[var(--text-tertiary)]",
        tone === "accent" && "border-[var(--accent)]/40 text-[var(--accent)]",
        tone === "warn" && "border-amber-500/40 text-amber-400",
      )}
    >
      {children}
    </span>
  );
}

export interface MenuItem {
  label: string;
  onSelect?: () => void;
  href?: string;
  destructive?: boolean;
  disabled?: boolean;
}

// RowMenu is the per-row ⋯ menu. Popover, not dropdown-menu: base-ui Menu
// breaks on first mount in the static export (see CLAUDE.md).
export function RowMenu({ label, items }: { label: string; items: MenuItem[] }) {
  const [open, setOpen] = useState(false);
  const visible = items.filter((i) => !i.disabled);
  if (visible.length === 0) return <span className="h-7 w-7" />;
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        aria-label={label}
        className="inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] data-[popup-open]:bg-[var(--bg-tertiary)]"
      >
        <MoreHorizontal className="h-3.5 w-3.5" />
      </PopoverTrigger>
      <PopoverContent align="end" className="w-52 gap-0 p-1">
        {visible.map((item) =>
          item.href ? (
            <Link
              key={item.label}
              href={item.href}
              className="rounded-md px-2 py-1.5 text-[12px] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
            >
              {item.label}
            </Link>
          ) : (
            <button
              key={item.label}
              type="button"
              onClick={() => {
                setOpen(false);
                item.onSelect?.();
              }}
              className={cn(
                "rounded-md px-2 py-1.5 text-left text-[12px] hover:bg-[var(--bg-tertiary)]",
                item.destructive
                  ? "text-red-400"
                  : "text-[var(--text-secondary)] hover:text-[var(--text-primary)]",
              )}
            >
              {item.label}
            </button>
          ),
        )}
      </PopoverContent>
    </Popover>
  );
}

export function AddMenu({
  onNew,
  onShared,
  onPaste,
  onGithub,
}: {
  onNew: () => void;
  onShared: () => void;
  onPaste: () => void;
  onGithub?: () => void;
}) {
  const [open, setOpen] = useState(false);
  const pick = (fn: () => void) => () => {
    setOpen(false);
    fn();
  };
  const items: { icon: ReactNode; label: string; run: () => void }[] = [
    { icon: <Plus className="h-3.5 w-3.5" />, label: "New variable", run: onNew },
    { icon: <KeyRound className="h-3.5 w-3.5" />, label: "Project secret", run: onShared },
    { icon: <ClipboardPaste className="h-3.5 w-3.5" />, label: "Paste .env", run: onPaste },
  ];
  if (onGithub) {
    items.push({ icon: <Github className="h-3.5 w-3.5" />, label: "Sign in with GitHub", run: onGithub });
  }
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={
          <Button variant="outline" size="sm" type="button">
            <Plus className="h-3.5 w-3.5" /> Add <ChevronDown className="h-3 w-3 opacity-60" />
          </Button>
        }
      />
      <PopoverContent align="end" className="w-48 gap-0 p-1">
        {items.map((i) => (
          <button
            key={i.label}
            type="button"
            onClick={pick(i.run)}
            className="flex items-center gap-2 rounded-md px-2 py-1.5 text-left text-[12px] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
          >
            {i.icon}
            {i.label}
          </button>
        ))}
      </PopoverContent>
    </Popover>
  );
}

// SharedPickerDialog chooses which project/instance secrets this service
// gets. It edits the same subscription list as `kuso env share|unshare`.
export function SharedPickerDialog({
  open,
  onOpenChange,
  project,
  shape,
  saving,
  onApply,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  project: string;
  shape: SubscribableShape | undefined;
  saving: boolean;
  onApply: (keys: string[]) => void;
}) {
  const [picked, setPicked] = useState<Set<string>>(new Set());
  useEffect(() => {
    if (open) setPicked(new Set(shape?.subscribed ?? []));
  }, [open, shape]);
  const sources = (shape?.sources ?? []).filter((s) => s.keys.length > 0);
  const toggle = (k: string) =>
    setPicked((prev) => {
      const next = new Set(prev);
      if (next.has(k)) next.delete(k);
      else next.add(k);
      return next;
    });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Project secrets</DialogTitle>
        </DialogHeader>
        {sources.length === 0 ? (
          <p className="text-[12px] text-[var(--text-tertiary)]">
            None yet.{" "}
            <Link
              href={`/projects/${encodeURIComponent(project)}/settings`}
              className="text-[var(--accent)] hover:underline"
            >
              Add one in project settings
            </Link>
          </p>
        ) : (
          <div className="max-h-[50vh] space-y-3 overflow-y-auto">
            {sources.map((src) => (
              <div key={src.secret}>
                <p className="mb-1 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                  {src.secret === INSTANCE_SHARED_SECRET ? "Instance" : "Project"}
                </p>
                {[...src.keys].sort().map((k) => (
                  <label
                    key={k}
                    className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 font-mono text-[12px] hover:bg-[var(--bg-tertiary)]"
                  >
                    <input
                      type="checkbox"
                      checked={picked.has(k)}
                      onChange={() => toggle(k)}
                      className="accent-[var(--accent)]"
                    />
                    {k}
                  </label>
                ))}
              </div>
            ))}
          </div>
        )}
        <DialogFooter>
          <Link
            href={`/projects/${encodeURIComponent(project)}/settings`}
            className="mr-auto self-center text-[12px] text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
          >
            Manage →
          </Link>
          <Button
            size="sm"
            disabled={saving || sources.length === 0}
            onClick={() => onApply(Array.from(picked).sort())}
          >
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function PasteEnvDialog({
  open,
  onOpenChange,
  onApply,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onApply: (text: string) => void;
}) {
  const [text, setText] = useState("");
  useEffect(() => {
    if (open) setText("");
  }, [open]);
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Paste .env</DialogTitle>
        </DialogHeader>
        <textarea
          autoFocus
          value={text}
          onChange={(e) => setText(e.target.value)}
          spellCheck={false}
          rows={10}
          placeholder={"API_URL=https://…\nNODE_ENV=production"}
          className="w-full resize-y rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-3 font-mono text-[12px] text-[var(--text-primary)] outline-none focus:border-[var(--border-strong)]"
        />
        <DialogFooter>
          <Button size="sm" disabled={!text.trim()} onClick={() => onApply(text)}>
            Add
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// DeployStatus is the one-line replacement for the old drift banners: quiet
// when the service is live, a short label while rolling or failed.
export function DeployStatus({ drift }: { drift: DriftReport | undefined }) {
  if (!drift) return null;
  const helmErr = drift.helmError?.trim();
  if (helmErr) {
    return (
      <span title={helmErr} className="inline-flex items-center gap-1.5 text-[11px] text-red-400">
        <span className="h-1.5 w-1.5 rounded-full bg-red-400" />
        Deploy failed
      </span>
    );
  }
  if (drift.rolloutPending || (drift.podsStale?.length ?? 0) > 0) {
    return (
      <span className="inline-flex items-center gap-1.5 text-[11px] text-blue-400">
        <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-blue-400" />
        Rolling out
      </span>
    );
  }
  return null;
}
