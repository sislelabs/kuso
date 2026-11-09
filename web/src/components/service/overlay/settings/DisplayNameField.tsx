"use client";

import { useEffect, useId, useState } from "react";
import { Check } from "lucide-react";
import { Input } from "@/components/ui/input";
import { useCanOnProject, Perms } from "@/features/auth";
import { usePatchService, useService } from "@/features/services";
import { describeError } from "@/lib/errors";
import { Field, inputClass } from "./_primitives";

const DISPLAY_NAME_RE = /^[A-Za-z0-9 -]{1,60}$/;

export function displayNameProblem(raw: string): string | null {
  const v = raw.trim();
  return v && !DISPLAY_NAME_RE.test(v) ? "Letters, digits, spaces and hyphens only, up to 60 characters" : null;
}

// DisplayNameField saves on its own when the field loses focus or on
// Enter: the label is cosmetic (canvas and header only), so it doesn't
// wait for a staged deploy and never restarts anything.
export function DisplayNameField({ project, service }: { project: string; service: string }) {
  const svc = useService(project, service);
  const saved = svc.data?.spec.displayName ?? "";
  const canWrite = useCanOnProject(project, Perms.ServicesWrite);
  const patch = usePatchService(project, service);
  const [value, setValue] = useState(saved);
  const [error, setError] = useState<string | null>(null);
  const [savedFlash, setSavedFlash] = useState(false);
  const hintId = useId();
  const errorId = useId();

  useEffect(() => setValue(saved), [saved]);
  useEffect(() => {
    if (!savedFlash) return;
    const t = window.setTimeout(() => setSavedFlash(false), 2500);
    return () => window.clearTimeout(t);
  }, [savedFlash]);

  const commit = async () => {
    const next = value.trim();
    if (next === saved.trim() || patch.isPending) return;
    const problem = displayNameProblem(next);
    if (problem) {
      setError(problem);
      return;
    }
    try {
      await patch.mutateAsync({ displayName: next });
      setError(null);
      setSavedFlash(true);
    } catch (e) {
      setError(describeError(e, "Couldn't save the display name"));
    }
  };

  return (
    <Field
      label={
        <span className="inline-flex items-center gap-2">
          Display name
          <span className="text-xs font-normal text-[var(--text-tertiary)]">Saves as you leave the field</span>
        </span>
      }
      htmlFor="svc-display-name"
      hint={<span id={hintId}>Shown on the canvas and in the header. Doesn&apos;t change URLs or DNS, and doesn&apos;t restart anything.</span>}
    >
      <div className="flex items-center gap-2">
        <Input
          id="svc-display-name"
          value={value}
          onChange={(e) => {
            setValue(e.target.value);
            setError(null);
          }}
          onBlur={() => void commit()}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              void commit();
            } else if (e.key === "Escape" && value !== saved) {
              e.stopPropagation();
              setValue(saved);
              setError(null);
            }
          }}
          placeholder={service}
          disabled={!canWrite || svc.isPending}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? `${hintId} ${errorId}` : hintId}
          aria-errormessage={error ? errorId : undefined}
          className={inputClass}
          maxLength={60}
          spellCheck={false}
        />
        <span aria-live="polite" className="w-16 shrink-0 text-xs">
          {patch.isPending ? (
            <span className="text-[var(--text-tertiary)]">Saving…</span>
          ) : savedFlash ? (
            <span className="inline-flex items-center gap-1 text-[var(--success)]">
              <Check aria-hidden className="h-3.5 w-3.5" />
              Saved
            </span>
          ) : null}
        </span>
      </div>
      {error && (
        <p id={errorId} role="alert" className="text-xs text-[var(--error)]">
          {error}
        </p>
      )}
    </Field>
  );
}
