"use client";

import { useId, useRef, useState } from "react";
import { AlertTriangle, Check, ChevronDown } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent } from "@/components/ui/popover";
import { useServiceBranches } from "@/features/services";
import { claimEscape } from "@/lib/escape-layer";
import { cn } from "@/lib/utils";
import { monoInputClass } from "./_primitives";
import { filterBranches, nextActive } from "./branchOptions";

// BranchInput is the branch field: a combobox over the repo's real
// branches that still takes any typed name. The list loads on first
// focus; while it's unavailable (older server, unreachable host) the field
// is plain free text. A name the remote doesn't have gets a warning, since
// that's a clone failure later.
export function BranchInput({
  project,
  service,
  id,
  value,
  onChange,
  placeholder,
  ariaLabel,
  className,
  suggest = true,
}: {
  project: string;
  service: string;
  id?: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  ariaLabel?: string;
  className?: string;
  // False while the repo URL itself has an unsaved edit: the list would
  // be the old repo's branches.
  suggest?: boolean;
}) {
  const [wanted, setWanted] = useState(false);
  const branches = useServiceBranches(project, service, wanted && suggest);
  const listId = useId();
  const warnId = useId();
  const optionId = (i: number) => `${listId}-opt-${i}`;
  const anchorRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const [open, setOpen] = useState(false);
  // What the user typed since opening; the list filters on this, not the
  // field value, so opening on "main" still shows every branch.
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(-1);

  const known = suggest ? branches.data : undefined;
  const names = known?.branches ?? [];
  const typed = value.trim();
  const missing = !!known && !known.truncated && typed !== "" && !names.includes(typed);
  const { shown, more } = filterBranches(names, query);
  const showList = open && names.length > 0;

  const openList = () => {
    setQuery("");
    setActive(-1);
    setOpen(true);
  };
  const pick = (name: string) => {
    onChange(name);
    setOpen(false);
    setActive(-1);
  };

  return (
    <div className="space-y-1.5">
      <div ref={anchorRef} className="relative">
        <Input
          ref={inputRef}
          id={id}
          role="combobox"
          aria-autocomplete="list"
          aria-expanded={showList}
          aria-controls={showList ? listId : undefined}
          aria-activedescendant={showList && active >= 0 ? optionId(active) : undefined}
          value={value}
          onFocus={() => {
            setWanted(true);
            openList();
          }}
          onClick={() => {
            if (!open) openList();
          }}
          onBlur={() => setOpen(false)}
          onChange={(e) => {
            onChange(e.target.value);
            setQuery(e.target.value);
            setActive(-1);
            setOpen(true);
          }}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown" || e.key === "ArrowUp") {
              if (names.length === 0) return;
              e.preventDefault();
              if (!open) {
                openList();
                return;
              }
              setActive((cur) => nextActive(cur, shown.length, e.key as "ArrowDown" | "ArrowUp"));
              return;
            }
            if (e.key === "Enter" && showList && active >= 0 && shown[active]) {
              e.preventDefault();
              pick(shown[active]);
              return;
            }
            if (e.key === "Escape" && showList) {
              claimEscape(e.nativeEvent);
              setOpen(false);
              return;
            }
            if (e.key === "Tab") setOpen(false);
          }}
          placeholder={placeholder}
          aria-label={ariaLabel}
          aria-describedby={missing ? warnId : undefined}
          spellCheck={false}
          autoComplete="off"
          className={cn(monoInputClass, names.length > 0 && "pr-8", className)}
        />
        {names.length > 0 && (
          <ChevronDown
            aria-hidden
            className="pointer-events-none absolute right-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-[var(--text-tertiary)]"
          />
        )}
      </div>
      <Popover
        open={showList}
        onOpenChange={(next, details) => {
          // Clicks on the field itself aren't "outside": it owns the list.
          const target = details.event?.target;
          if (!next && target instanceof Node && anchorRef.current?.contains(target)) return;
          if (!next) setOpen(false);
        }}
      >
        <PopoverContent
          anchor={anchorRef}
          align="start"
          initialFocus={false}
          finalFocus={false}
          role="presentation"
          className="w-(--anchor-width) min-w-56 gap-0 p-1"
        >
          <div
            id={listId}
            role="listbox"
            aria-label="Remote branches"
            // Scrolling or clicking the list must not pull focus off the field.
            onMouseDown={(e) => e.preventDefault()}
            className="max-h-64 overflow-y-auto"
          >
            {shown.length === 0 ? (
              <p className="px-2 py-1.5 text-xs text-[var(--text-tertiary)]">
                No branch matches. Keep typing to use <span className="font-mono">{typed || "a new name"}</span>.
              </p>
            ) : (
              shown.map((b, i) => (
                <div
                  key={b}
                  id={optionId(i)}
                  role="option"
                  aria-selected={b === typed}
                  onMouseDown={() => pick(b)}
                  onMouseEnter={() => setActive(i)}
                  className={cn(
                    "flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 font-mono text-xs text-[var(--text-primary)]",
                    i === active && "bg-[var(--bg-tertiary)]",
                  )}
                >
                  <span className="min-w-0 flex-1 truncate">{b}</span>
                  {b === typed && <Check aria-hidden className="h-3.5 w-3.5 shrink-0 text-[var(--text-secondary)]" />}
                </div>
              ))
            )}
          </div>
          {more > 0 && (
            <p className="border-t border-[var(--border-subtle)] px-2 pt-1.5 pb-1 text-xs text-[var(--text-tertiary)]">
              {more} more. Type to narrow the list.
            </p>
          )}
        </PopoverContent>
      </Popover>
      {missing ? (
        <p id={warnId} className="flex items-start gap-1.5 text-xs text-[var(--warning)]">
          <AlertTriangle aria-hidden className="mt-px h-3.5 w-3.5 shrink-0" />
          <span>
            The repository has no branch named <span className="font-mono">{typed}</span>, so a build would fail to clone.
          </span>
        </p>
      ) : wanted && known ? (
        <p className="text-xs text-[var(--text-tertiary)]">
          {names.length} {names.length === 1 ? "branch" : "branches"} on the remote
          {known.truncated ? " (first 1000)" : ""}. Pick one or type a name.
        </p>
      ) : wanted && suggest && branches.isPending ? (
        <p className="text-xs text-[var(--text-tertiary)]">Loading branches…</p>
      ) : null}
    </div>
  );
}
