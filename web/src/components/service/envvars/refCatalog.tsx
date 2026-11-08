"use client";

import { forwardRef, useEffect, useId, useImperativeHandle, useMemo, useRef, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Boxes, ChevronLeft, Database, Link2, Search } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { useAddons, useProject } from "@/features/projects";
import { listAddonSecretKeys } from "@/features/services/api";
import { cloneBaseName, groupReferenceNames, resourceGroup } from "@/lib/env-group";
import { claimEscape } from "@/lib/escape-layer";
import { cn } from "@/lib/utils";
import { ENV_NAME_RE } from "@/components/service/envVarTransforms";
import { applyRefPick, findRefTrigger, rankMatches } from "./refAutocomplete";

export type RefTargetKind = "service" | "addon";
export interface RefTarget {
  name: string;
  kind: RefTargetKind;
}

// Keys the server resolves for a service ref (server-go varrefs.go).
export const SERVICE_REF_KEYS: { key: string; hint: string }[] = [
  { key: "INTERNAL_URL", hint: "In-cluster URL, for backend to backend" },
  { key: "PUBLIC_URL", hint: "Public https URL, for browsers" },
  { key: "HOST", hint: "In-cluster hostname" },
  { key: "PORT", hint: "Container port" },
  { key: "PUBLIC_HOST", hint: "Public hostname" },
  { key: "URL", hint: "Same as INTERNAL_URL" },
];

// useRefTargets lists what a ${{ }} ref can point at, scoped to the edited
// service's env-group: inside a group the server resolves base names to
// that group's clones, so clones are offered by base name.
export function useRefTargets(project: string, excludeService: string): { targets: RefTarget[]; loading: boolean } {
  const proj = useProject(project);
  const addons = useAddons(project);
  const targets = useMemo(() => {
    const prefix = project + "-";
    const short = (fqn: string) => (fqn.startsWith(prefix) ? fqn.slice(prefix.length) : fqn);
    const svcList = proj.data?.services ?? [];
    const self = svcList.find((s) => short(s.metadata.name) === excludeService);
    const group = resourceGroup(self?.metadata.labels);
    const selfBase = cloneBaseName(excludeService, group);
    const services = groupReferenceNames(
      svcList.map((s) => ({ name: short(s.metadata.name), labels: s.metadata.labels })),
      group,
    ).filter((s) => s !== selfBase);
    const addonNames = groupReferenceNames(
      (addons.data ?? []).map((a) => ({ name: short(a.metadata.name), labels: a.metadata.labels })),
      group,
    );
    return [
      ...addonNames.map((name): RefTarget => ({ name, kind: "addon" })),
      ...services.map((name): RefTarget => ({ name, kind: "service" })),
    ];
  }, [proj.data, addons.data, project, excludeService]);
  return { targets, loading: proj.isPending || addons.isPending };
}

// useRefKeys returns the keys a target exposes. Addon keys come from the
// conn Secret and are fetched only once a target is chosen.
export function useRefKeys(
  project: string,
  target: RefTarget | null,
): { keys: { key: string; hint?: string }[]; loading: boolean; error: boolean } {
  const isAddon = target?.kind === "addon";
  const q = useQuery({
    queryKey: ["addons", project, target?.name ?? "", "secret-keys"],
    queryFn: () => listAddonSecretKeys(project, target?.name ?? ""),
    enabled: isAddon,
    staleTime: 60_000,
  });
  if (!target) return { keys: [], loading: false, error: false };
  if (!isAddon) return { keys: SERVICE_REF_KEYS, loading: false, error: false };
  return {
    keys: (q.data?.keys ?? []).slice().sort().map((key) => ({ key })),
    loading: q.isPending,
    error: q.isError,
  };
}

function TargetIcon({ kind }: { kind: RefTargetKind }) {
  return kind === "addon" ? (
    <Database className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
  ) : (
    <Boxes className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
  );
}

function suggestedName(target: RefTarget, key: string): string {
  if (target.kind === "addon") return key;
  const base = target.name.toUpperCase().replace(/[^A-Z0-9]+/g, "_").replace(/^_+|_+$/g, "");
  return `${base}_${key}`;
}

// AddReferenceDialog: pick a target → key → variable name. Inserts a new
// row whose value is the ${{ target.KEY }} ref.
export function AddReferenceDialog({
  open,
  onOpenChange,
  project,
  service,
  existingNames,
  onInsert,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  project: string;
  service: string;
  existingNames: ReadonlySet<string>;
  onInsert: (name: string, value: string) => void;
}) {
  const { targets, loading } = useRefTargets(project, service);
  const [target, setTarget] = useState<RefTarget | null>(null);
  const [key, setKey] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [filter, setFilter] = useState("");
  const keys = useRefKeys(project, target);
  useEffect(() => {
    if (!open) return;
    setTarget(null);
    setKey(null);
    setName("");
    setFilter("");
  }, [open]);

  const shownTargets = rankMatches(targets, filter, (t) => t.name);
  const shownKeys = rankMatches(keys.keys, filter, (k) => k.key);
  const nameErr = !name
    ? ""
    : !ENV_NAME_RE.test(name)
      ? "Letters, digits and _ only; can't start with a digit"
      : existingNames.has(name)
        ? `${name} already exists. Inserting replaces its value`
        : "";
  const ref = target && key ? `\${{ ${target.name}.${key} }}` : "";

  const pickKey = (k: string) => {
    if (!target) return;
    setKey(k);
    setName(suggestedName(target, k));
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Add reference</DialogTitle>
        </DialogHeader>
        <p className="text-xs text-[var(--text-secondary)]">
          A reference stays in sync with its source: addon credentials rotate and service URLs follow renames without
          you editing this variable.
        </p>
        {!key ? (
          <div className="space-y-2">
            <div className="flex items-center gap-1.5">
              {target && (
                <button
                  type="button"
                  onClick={() => {
                    setTarget(null);
                    setFilter("");
                  }}
                  aria-label="Back to sources"
                  className="inline-flex h-8 w-8 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
                >
                  <ChevronLeft className="h-4 w-4" />
                </button>
              )}
              <div className="relative flex-1">
                <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-[var(--text-tertiary)]" />
                <Input
                  autoFocus
                  value={filter}
                  onChange={(e) => setFilter(e.target.value)}
                  placeholder={target ? `Keys of ${target.name}` : "Find an addon or service"}
                  aria-label={target ? `Filter keys of ${target.name}` : "Filter addons and services"}
                  className="h-8 pl-7 text-xs"
                />
              </div>
            </div>
            <div className="max-h-[45vh] overflow-y-auto rounded-md border border-[var(--border-subtle)]" role="listbox">
              {!target ? (
                loading ? (
                  <p className="px-3 py-4 text-xs text-[var(--text-tertiary)]">Loading…</p>
                ) : shownTargets.length === 0 ? (
                  <p className="px-3 py-4 text-xs text-[var(--text-tertiary)]">
                    {targets.length === 0 ? "No addons or other services in this project yet." : "No match."}
                  </p>
                ) : (
                  shownTargets.map((t) => (
                    <PickRow
                      key={`${t.kind}:${t.name}`}
                      onClick={() => {
                        setTarget(t);
                        setFilter("");
                      }}
                      icon={<TargetIcon kind={t.kind} />}
                      label={t.name}
                      hint={t.kind === "addon" ? "addon" : "service"}
                    />
                  ))
                )
              ) : keys.loading ? (
                <p className="px-3 py-4 text-xs text-[var(--text-tertiary)]">Loading keys…</p>
              ) : keys.error ? (
                <p className="px-3 py-4 text-xs text-[var(--warning)]">
                  {target.name} has no connection keys yet. It may still be provisioning.
                </p>
              ) : shownKeys.length === 0 ? (
                <p className="px-3 py-4 text-xs text-[var(--text-tertiary)]">No match.</p>
              ) : (
                shownKeys.map((k) => (
                  <PickRow key={k.key} onClick={() => pickKey(k.key)} label={k.key} hint={k.hint} mono />
                ))
              )}
            </div>
          </div>
        ) : (
          <div className="space-y-3">
            <div className="flex items-center gap-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-2">
              <Link2 className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
              <code className="min-w-0 flex-1 truncate font-mono text-xs text-[var(--text-primary)]">{ref}</code>
              <button
                type="button"
                onClick={() => setKey(null)}
                className="text-xs text-[var(--text-secondary)] hover:text-[var(--text-primary)]"
              >
                Change
              </button>
            </div>
            <label className="block space-y-1">
              <span className="text-xs font-medium text-[var(--text-secondary)]">Variable name</span>
              <Input
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                spellCheck={false}
                aria-invalid={!!nameErr && !ENV_NAME_RE.test(name)}
                className="h-8 font-mono text-xs"
              />
              {nameErr && (
                <span
                  className={cn(
                    "block text-xs",
                    ENV_NAME_RE.test(name) ? "text-[var(--warning)]" : "text-[var(--error)]",
                  )}
                >
                  {nameErr}
                </span>
              )}
            </label>
          </div>
        )}
        <DialogFooter>
          <Button size="sm" variant="ghost" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            size="sm"
            disabled={!ref || !ENV_NAME_RE.test(name)}
            onClick={() => {
              onInsert(name, ref);
              onOpenChange(false);
            }}
          >
            Insert
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function PickRow({
  onClick,
  icon,
  label,
  hint,
  mono,
}: {
  onClick: () => void;
  icon?: ReactNode;
  label: string;
  hint?: string;
  mono?: boolean;
}) {
  return (
    <button
      type="button"
      role="option"
      aria-selected={false}
      onClick={onClick}
      className="flex w-full items-center gap-2 border-b border-[var(--border-subtle)] px-3 py-2 text-left last:border-b-0 hover:bg-[var(--bg-tertiary)] focus-visible:bg-[var(--bg-tertiary)] focus-visible:outline-none"
    >
      {icon}
      <span className={cn("min-w-0 flex-1 truncate text-xs text-[var(--text-primary)]", mono && "font-mono")}>
        {label}
      </span>
      {hint && <span className="shrink-0 text-xs text-[var(--text-tertiary)]">{hint}</span>}
    </button>
  );
}

export interface RefValueInputHandle {
  focus: () => void;
}

// RefValueInput is the value field with Railway-style `${{` autocomplete:
// typing "${{" lists addons/services, "${{ db." lists db's keys, and a
// pick writes the complete ref. Arrow keys + Enter/Tab drive the list.
export const RefValueInput = forwardRef<
  RefValueInputHandle,
  {
    project: string;
    service: string;
    value: string;
    onChange: (v: string) => void;
    placeholder?: string;
    ariaLabel: string;
    disabled?: boolean;
    autoFocus?: boolean;
    className?: string;
    onSubmit?: () => void;
  }
>(function RefValueInput(
  { project, service, value, onChange, placeholder, ariaLabel, disabled, autoFocus, className, onSubmit },
  ref,
) {
  const inputRef = useRef<HTMLInputElement>(null);
  useImperativeHandle(ref, () => ({ focus: () => inputRef.current?.focus() }));
  const [caret, setCaret] = useState(0);
  const [focused, setFocused] = useState(false);
  const [dismissedAt, setDismissedAt] = useState<number | null>(null);
  const [active, setActive] = useState(0);
  const listId = useId();
  const trigger = focused ? findRefTrigger(value, caret) : null;
  const open = !!trigger && dismissedAt !== trigger.start;
  const { targets } = useRefTargets(project, service);
  const target = useMemo<RefTarget | null>(() => {
    if (!trigger || trigger.key === null) return null;
    return targets.find((t) => t.name === trigger.target) ?? null;
  }, [targets, trigger]);
  const keys = useRefKeys(project, target);

  type Option = { label: string; hint?: string; kind?: RefTargetKind; pick: { target: string; key?: string } };
  const options: Option[] = !trigger
    ? []
    : trigger.key === null
      ? rankMatches(targets, trigger.target, (t) => t.name).map((t) => ({
          label: t.name,
          hint: t.kind,
          kind: t.kind,
          pick: { target: t.name },
        }))
      : target
        ? rankMatches(keys.keys, trigger.key, (k) => k.key).map((k) => ({
            label: k.key,
            hint: k.hint,
            pick: { target: target.name, key: k.key },
          }))
        : [];
  const activeIdx = Math.min(active, Math.max(0, options.length - 1));

  // Reset the highlighted option whenever the list itself changes kind.
  const listKey = trigger ? `${trigger.start}:${trigger.key === null ? "t" : "k"}:${trigger.target}` : "";
  useEffect(() => setActive(0), [listKey]);

  const syncCaret = () => setCaret(inputRef.current?.selectionStart ?? value.length);

  const choose = (o: Option) => {
    if (!trigger) return;
    const next = applyRefPick(value, caret, trigger, o.pick);
    onChange(next.value);
    setCaret(next.caret);
    requestAnimationFrame(() => {
      const el = inputRef.current;
      if (!el) return;
      el.focus();
      el.setSelectionRange(next.caret, next.caret);
    });
  };

  const emptyText = !trigger
    ? ""
    : trigger.key === null
      ? targets.length === 0
        ? "No addons or other services to reference"
        : "No match"
      : !target
        ? `No addon or service named "${trigger.target}"`
        : keys.loading
          ? "Loading keys…"
          : keys.error
            ? `${target.name} has no keys yet`
            : "No match";

  return (
    <div className="relative min-w-0 flex-1">
      <Input
        ref={inputRef}
        autoFocus={autoFocus}
        value={value}
        placeholder={placeholder}
        aria-label={ariaLabel}
        disabled={disabled}
        spellCheck={false}
        autoComplete="off"
        role="combobox"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        aria-autocomplete="list"
        onFocus={() => {
          setFocused(true);
          syncCaret();
        }}
        onBlur={() => setFocused(false)}
        onChange={(e) => {
          onChange(e.target.value);
          setCaret(e.target.selectionStart ?? e.target.value.length);
          setDismissedAt(null);
        }}
        onSelect={syncCaret}
        onKeyDown={(e) => {
          if (open && options.length > 0) {
            if (e.key === "ArrowDown") {
              e.preventDefault();
              setActive((activeIdx + 1) % options.length);
              return;
            }
            if (e.key === "ArrowUp") {
              e.preventDefault();
              setActive((activeIdx - 1 + options.length) % options.length);
              return;
            }
            if (e.key === "Enter" || e.key === "Tab") {
              e.preventDefault();
              choose(options[activeIdx]);
              return;
            }
          }
          if (open && e.key === "Escape") {
            claimEscape(e.nativeEvent);
            setDismissedAt(trigger?.start ?? null);
            return;
          }
          if (e.key === "Enter" && onSubmit) {
            e.preventDefault();
            onSubmit();
          }
        }}
        className={className}
      />
      {open && (
        <div
          id={listId}
          role="listbox"
          data-escape-layer
          aria-label="Reference suggestions"
          className="absolute left-0 right-0 top-full z-50 mt-1 max-h-60 overflow-y-auto rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] p-1 shadow-[var(--shadow-lg)]"
        >
          <p className="px-2 pb-1 pt-0.5 text-xs text-[var(--text-tertiary)]">
            {trigger?.key === null ? "Reference an addon or service" : `Keys of ${trigger?.target}`}
          </p>
          {options.length === 0 ? (
            <p className="px-2 py-1.5 text-xs text-[var(--text-tertiary)]">{emptyText}</p>
          ) : (
            options.map((o, i) => (
              <button
                key={o.label}
                type="button"
                role="option"
                aria-selected={i === activeIdx}
                // mousedown keeps focus in the input so the caret survives.
                onMouseDown={(e) => {
                  e.preventDefault();
                  choose(o);
                }}
                onMouseEnter={() => setActive(i)}
                className={cn(
                  "flex w-full items-center gap-2 rounded px-2 py-1.5 text-left",
                  i === activeIdx ? "bg-[var(--bg-tertiary)]" : "hover:bg-[var(--bg-tertiary)]",
                )}
              >
                {o.kind && <TargetIcon kind={o.kind} />}
                <span className="min-w-0 flex-1 truncate font-mono text-xs text-[var(--text-primary)]">{o.label}</span>
                {o.hint && <span className="shrink-0 truncate text-xs text-[var(--text-tertiary)]">{o.hint}</span>}
              </button>
            ))
          )}
        </div>
      )}
    </div>
  );
});
