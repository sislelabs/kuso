"use client";

import { useId, useState } from "react";
import { AlertTriangle } from "lucide-react";
import { Input } from "@/components/ui/input";
import { useServiceBranches } from "@/features/services";
import { cn } from "@/lib/utils";
import { monoInputClass } from "./_primitives";

// BranchInput is the branch field with the repo's real branches as
// suggestions. The list loads on first focus; while it's unavailable
// (older server, unreachable host) the field stays free text. A name the
// remote doesn't have gets a warning, since that's a clone failure later.
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
  const known = suggest ? branches.data : undefined;
  const names = known?.branches ?? [];
  const typed = value.trim();
  const missing = !!known && !known.truncated && typed !== "" && !names.includes(typed);
  return (
    <div className="space-y-1.5">
      <Input
        id={id}
        list={names.length > 0 ? listId : undefined}
        value={value}
        onFocus={() => setWanted(true)}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        aria-label={ariaLabel}
        aria-describedby={missing ? warnId : undefined}
        spellCheck={false}
        autoComplete="off"
        className={cn(monoInputClass, className)}
      />
      {names.length > 0 && (
        <datalist id={listId}>
          {names.map((b) => (
            <option key={b} value={b} />
          ))}
        </datalist>
      )}
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
