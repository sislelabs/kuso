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

// Panel-level chords live in their own components, but they're listed
// here too so "?" answers "what can I press" on any page.
const SERVICE_PANEL: Shortcut[] = [
  { keys: ["G", "D"], sequence: true, description: "Deployments" },
  { keys: ["G", "V"], sequence: true, description: "Variables" },
  { keys: ["G", "L"], sequence: true, description: "Logs" },
  { keys: ["G", "M"], sequence: true, description: "Metrics" },
  { keys: ["G", "E"], sequence: true, description: "Errors" },
  { keys: ["G", "U"], sequence: true, description: "Vulnerabilities" },
  { keys: ["G", "T"], sequence: true, description: "Shell" },
  { keys: ["G", "C"], sequence: true, description: "Crons" },
  { keys: ["G", "R"], sequence: true, description: "Runs" },
  { keys: ["G", "S"], sequence: true, description: "Settings" },
  { keys: ["Esc"], description: "Close the panel" },
];
const ADDON_PANEL: Shortcut[] = [
  { keys: ["G", "O"], sequence: true, description: "Overview" },
  { keys: ["G", "B"], sequence: true, description: "Backups" },
  { keys: ["G", "Q"], sequence: true, description: "Data (browse and query)" },
  { keys: ["G", "U"], sequence: true, description: "Vulnerabilities" },
  { keys: ["G", "S"], sequence: true, description: "Settings" },
];
const PALETTE: Shortcut[] = [
  { keys: ["↑", "↓"], description: "Move through results" },
  { keys: ["↵"], description: "Open or run the selected item" },
  { keys: ["⌫"], description: "Up a level (when the search is empty)" },
  { keys: ["Esc"], description: "Back, or close the palette" },
];

// A page that owns a shortcut group (the canvas) fires a cancelable
// `kuso:shortcuts` event from its help button. Claim it only when the
// group is already in the registry (`detail.registered`): the dialog then
// lists it from there, so adding the announced copy would duplicate it.
function isRegisteredAnnouncement(detail: unknown): boolean {
  return typeof detail === "object" && detail !== null && (detail as { registered?: unknown }).registered === true;
}

function ShortcutsDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const registered = useShortcutGroups();
  const [mod, setMod] = useState("Ctrl");
  useEffect(() => setMod(isMac() ? "⌘" : "Ctrl"), []);

  useEffect(() => {
    const onAnnounce = (e: Event) => {
      if (!isRegisteredAnnouncement((e as CustomEvent<unknown>).detail)) return;
      e.preventDefault();
      setShortcutsHelpOpen(true);
    };
    window.addEventListener("kuso:shortcuts", onAnnounce);
    return () => window.removeEventListener("kuso:shortcuts", onAnnounce);
  }, []);

  const global: Shortcut[] = [
    { keys: [mod, "K"], description: "Search and run commands" },
    { keys: ["/"], description: "Focus search on this page" },
    { keys: ["?"], description: "Show keyboard shortcuts" },
    ...Object.entries(GO_TO).map(([k, v]) => ({ keys: ["G", k.toUpperCase()], sequence: true, description: v.label })),
    { keys: ["Esc"], description: "Close dialog, panel or overlay" },
  ];

  // Page sections first: they're what the person was looking at.
  const sections = [
    ...registered,
    { title: "Everywhere", items: global },
    { title: "Command palette", items: PALETTE },
    { title: "Service panel", items: SERVICE_PANEL },
    { title: "Database panel", items: ADDON_PANEL },
  ];

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="gap-0 p-0 sm:max-w-3xl">
        <DialogHeader className="border-b border-[var(--border-subtle)] px-5 py-4">
          <DialogTitle>Keyboard shortcuts</DialogTitle>
          <DialogDescription>Shortcuts work anywhere except while you&apos;re typing in a field.</DialogDescription>
        </DialogHeader>
        <div className="grid grid-cols-1 gap-x-8 gap-y-6 px-5 py-5 sm:grid-cols-2">
          {sections.map((g, i) => (
            <ShortcutList key={`${g.title}-${i}`} title={g.title} items={g.items} />
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
