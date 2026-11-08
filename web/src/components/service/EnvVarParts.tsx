"use client";

import { useEffect, useState, type ReactNode } from "react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { ChevronDown, ClipboardPaste, FileDown, Github, KeyRound, Link2, Lock, MoreHorizontal, Plus } from "lucide-react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
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
        "shrink-0 rounded border px-1.5 py-px text-xs leading-4",
        tone === "neutral" && "border-[var(--border-subtle)] text-[var(--text-tertiary)]",
        tone === "accent" && "border-[var(--accent)]/40 text-[var(--accent)]",
        tone === "warn" && "border-[var(--warning)]/40 text-[var(--warning)]",
      )}
    >
      {children}
    </span>
  );
}

// SecretLock marks a row whose value is a secret (masked by default).
export function SecretLock() {
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <span
            role="img"
            aria-label="Secret — value hidden"
            className="inline-flex shrink-0 items-center text-[var(--text-tertiary)]"
          />
        }
      >
        <Lock className="h-3 w-3" aria-hidden />
      </TooltipTrigger>
      <TooltipContent>Secret — value hidden</TooltipContent>
    </Tooltip>
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
              className="rounded-md px-2 py-1.5 text-xs text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
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
                "rounded-md px-2 py-1.5 text-left text-xs hover:bg-[var(--bg-tertiary)]",
                item.destructive
                  ? "text-[var(--error)]"
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
  onReference,
  onShared,
  onPaste,
  onImportExample,
  onGithub,
}: {
  onNew: () => void;
  onReference?: () => void;
  onShared: () => void;
  onPaste: () => void;
  onImportExample?: () => void;
  onGithub?: () => void;
}) {
  const [open, setOpen] = useState(false);
  const pick = (fn: () => void) => () => {
    setOpen(false);
    fn();
  };
  const items: { icon: ReactNode; label: string; hint: string; run: () => void }[] = [
    { icon: <Plus className="h-3.5 w-3.5" />, label: "New variable", hint: "A plain KEY=value", run: onNew },
  ];
  if (onReference) {
    items.push({
      icon: <Link2 className="h-3.5 w-3.5" />,
      label: "Reference",
      hint: "An addon key or another service's URL",
      run: onReference,
    });
  }
  items.push(
    { icon: <KeyRound className="h-3.5 w-3.5" />, label: "Project secret", hint: "Shared across services", run: onShared },
    { icon: <ClipboardPaste className="h-3.5 w-3.5" />, label: "Paste .env", hint: "Many at once", run: onPaste },
  );
  if (onImportExample) {
    items.push({
      icon: <FileDown className="h-3.5 w-3.5" />,
      label: "Import .env.example",
      hint: "Keys from your repo, never overwrites",
      run: onImportExample,
    });
  }
  if (onGithub) {
    items.push({
      icon: <Github className="h-3.5 w-3.5" />,
      label: "Sign in with GitHub",
      hint: "Register an OAuth app",
      run: onGithub,
    });
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
      <PopoverContent align="end" className="w-64 gap-0 p-1">
        {items.map((i) => (
          <button
            key={i.label}
            type="button"
            onClick={pick(i.run)}
            className="flex items-start gap-2.5 rounded-md px-2 py-1.5 text-left text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
          >
            <span className="mt-0.5">{i.icon}</span>
            <span className="min-w-0">
              <span className="block text-xs font-medium text-[var(--text-primary)]">{i.label}</span>
              <span className="block text-xs text-[var(--text-tertiary)]">{i.hint}</span>
            </span>
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
          <p className="text-xs text-[var(--text-tertiary)]">
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
                <p className="mb-1 text-xs font-medium text-[var(--text-secondary)]">
                  {src.secret === INSTANCE_SHARED_SECRET ? "Instance" : "Project"}
                </p>
                {[...src.keys].sort().map((k) => (
                  <label
                    key={k}
                    className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 font-mono text-xs hover:bg-[var(--bg-tertiary)]"
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
            className="mr-auto self-center text-xs text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
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

// onApply returns an error message to keep the dialog open with the text
// intact (malformed lines), or null once the paste was staged.
export function PasteEnvDialog({
  open,
  onOpenChange,
  onApply,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onApply: (text: string) => string | null;
}) {
  const [text, setText] = useState("");
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (open) {
      setText("");
      setError(null);
    }
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
          onChange={(e) => {
            setText(e.target.value);
            setError(null);
          }}
          spellCheck={false}
          rows={10}
          aria-label=".env contents"
          placeholder={"API_URL=https://…\nNODE_ENV=production"}
          className="w-full resize-y rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-3 font-mono text-xs text-[var(--text-primary)] outline-none placeholder:text-[var(--text-tertiary)] focus:border-[var(--border-strong)]"
        />
        {error ? (
          <p role="alert" className="text-xs text-[var(--error)]">
            {error}
          </p>
        ) : (
          <p className="text-xs text-[var(--text-tertiary)]">
            Existing names are overwritten. Nothing is saved until you review and save.
          </p>
        )}
        <DialogFooter>
          <Button size="sm" disabled={!text.trim()} onClick={() => setError(onApply(text))}>
            Stage variables
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
      <span title={helmErr} className="inline-flex items-center gap-1.5 text-xs text-[var(--error)]">
        <span className="h-1.5 w-1.5 rounded-full bg-[var(--error)]" />
        Deploy failed
      </span>
    );
  }
  if (drift.rolloutPending || (drift.podsStale?.length ?? 0) > 0) {
    return (
      <span className="inline-flex items-center gap-1.5 text-xs text-[var(--info)]">
        <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-[var(--info)]" />
        Rolling out
      </span>
    );
  }
  return null;
}
