"use client";

import { LayoutList, Workflow } from "lucide-react";
import { cn } from "@/lib/utils";
import type { ProjectViewMode } from "./projectViewPref";

const OPTIONS: { value: ProjectViewMode; label: string; Icon: typeof Workflow }[] = [
  { value: "canvas", label: "Canvas", Icon: Workflow },
  { value: "list", label: "List", Icon: LayoutList },
];

export function ViewToggle({ value, onChange }: { value: ProjectViewMode; onChange: (v: ProjectViewMode) => void }) {
  return (
    <div
      role="radiogroup"
      aria-label="Project view"
      className="inline-flex items-center rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-0.5"
      onKeyDown={(e) => {
        if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
        e.preventDefault();
        const next = value === "canvas" ? "list" : "canvas";
        onChange(next);
        const el = e.currentTarget.querySelector<HTMLButtonElement>(`[data-value="${next}"]`);
        el?.focus();
      }}
    >
      {OPTIONS.map(({ value: v, label, Icon }) => {
        const on = v === value;
        return (
          <button
            key={v}
            type="button"
            role="radio"
            aria-checked={on}
            data-value={v}
            tabIndex={on ? 0 : -1}
            onClick={() => onChange(v)}
            className={cn(
              "inline-flex h-6 items-center gap-1.5 rounded px-2 text-xs font-medium transition-colors",
              on
                ? "bg-[var(--bg-elevated)] text-[var(--text-primary)] shadow-[var(--shadow-sm)]"
                : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]",
            )}
          >
            <Icon className="h-3.5 w-3.5" aria-hidden />
            {label}
          </button>
        );
      })}
    </div>
  );
}
