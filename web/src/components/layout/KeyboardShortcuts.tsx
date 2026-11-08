"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { KbdCombo } from "@/components/ui/kbd";
import {
  focusPrimarySearch,
  isMac,
  isTypingTarget,
  setShortcutsHelpOpen,
  useShortcutGroups,
  useShortcutsHelpOpen,
  type Shortcut,
} from "@/lib/shortcuts";

const GO_TO: Record<string, { href: string; label: string }> = {
  p: { href: "/projects", label: "Go to projects" },
  s: { href: "/settings", label: "Go to settings" },
  m: { href: "/marketplace", label: "Go to marketplace" },
};
const SEQUENCE_TIMEOUT_MS = 1200;

// Global keyboard layer, mounted once in the dashboard shell:
//   ?      open the shortcuts help (lists global + page-registered keys)
//   /      focus the page's primary search ([data-primary-search])
//   g p/s/m  go to projects / settings / marketplace
// Keys typed into fields, or with ⌘/Ctrl/Alt held, are left alone.
export function KeyboardShortcuts() {
  const router = useRouter();
  const open = useShortcutsHelpOpen();
  const pendingG = useRef<number | null>(null);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return;
      if (isTypingTarget(e.target)) return;
      // Leave keys alone while another modal (palette, confirm) owns focus.
      if (!open && document.querySelector("[role=dialog][data-open], [role=alertdialog]")) return;

      if (pendingG.current !== null) {
        window.clearTimeout(pendingG.current);
        pendingG.current = null;
        const dest = GO_TO[e.key.toLowerCase()];
        if (dest) {
          e.preventDefault();
          router.push(dest.href);
          return;
        }
      }
      if (e.key === "?") {
        e.preventDefault();
        setShortcutsHelpOpen(!open);
      } else if (e.key === "/" && !open) {
        if (focusPrimarySearch()) e.preventDefault();
      } else if (e.key === "g" && !open) {
        pendingG.current = window.setTimeout(() => {
          pendingG.current = null;
        }, SEQUENCE_TIMEOUT_MS);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      if (pendingG.current !== null) window.clearTimeout(pendingG.current);
    };
  }, [open, router]);

  return <ShortcutsDialog open={open} onOpenChange={setShortcutsHelpOpen} />;
}

function ShortcutsDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const groups = useShortcutGroups();
  const [mod, setMod] = useState("Ctrl");
  useEffect(() => setMod(isMac() ? "⌘" : "Ctrl"), []);

  const global: Shortcut[] = [
    { keys: [mod, "K"], description: "Search and run commands" },
    { keys: ["/"], description: "Focus search on this page" },
    { keys: ["?"], description: "Show keyboard shortcuts" },
    ...Object.entries(GO_TO).map(([k, v]) => ({ keys: ["G", k.toUpperCase()], sequence: true, description: v.label })),
    { keys: ["Esc"], description: "Close dialog, panel or overlay" },
  ];

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={groups.length > 0 ? "gap-0 p-0 sm:max-w-2xl" : "gap-0 p-0 sm:max-w-md"}>
        <DialogHeader className="border-b border-[var(--border-subtle)] px-5 py-4">
          <DialogTitle>Keyboard shortcuts</DialogTitle>
          <DialogDescription>Shortcuts work anywhere except while you&apos;re typing in a field.</DialogDescription>
        </DialogHeader>
        <div className={groups.length > 0 ? "grid gap-x-8 gap-y-6 px-5 py-5 sm:grid-cols-2" : "px-5 py-5"}>
          <ShortcutList title="Everywhere" items={global} />
          {groups.map((g) => (
            <ShortcutList key={g.id} title={g.title} items={g.items} />
          ))}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function ShortcutList({ title, items }: { title: string; items: readonly Shortcut[] }) {
  return (
    <section>
      <h3 className="mb-2 text-xs font-medium text-[var(--text-tertiary)]">{title}</h3>
      <dl className="space-y-1.5">
        {items.map((s) => (
          <div key={s.description + s.keys.join("+")} className="flex items-center justify-between gap-4">
            <dt className="text-[13px] text-[var(--text-secondary)]">{s.description}</dt>
            <dd className="shrink-0">
              <KbdCombo keys={s.keys} sequence={s.sequence} />
            </dd>
          </div>
        ))}
      </dl>
    </section>
  );
}
