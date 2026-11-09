import { cloneElement, isValidElement, type ReactElement, type ReactNode } from "react";

// FormField: label and hint above a full-width control, error below.
// The hint sits under the label so it reads before the user types.
export function FormField({
  label,
  hint,
  error,
  htmlFor,
  optional,
  children,
}: {
  label: string;
  hint?: ReactNode;
  error?: string | null;
  // Set when the field wraps a single input; button groups have no
  // single control to point a <label> at.
  htmlFor?: string;
  optional?: boolean;
  children: ReactNode;
}) {
  const labelClass = "text-[13px] font-medium text-[var(--text-primary)]";
  // With a single control, its hint and error are tied to it so a screen
  // reader reads them on focus, and the error marks it invalid.
  const hintId = htmlFor && hint ? `${htmlFor}-hint` : undefined;
  const errorId = htmlFor && error ? `${htmlFor}-error` : undefined;
  const control =
    htmlFor && isValidElement(children)
      ? cloneElement(children as ReactElement<Record<string, unknown>>, {
          "aria-describedby": [hintId, errorId].filter(Boolean).join(" ") || undefined,
          ...(error ? { "aria-invalid": true, "aria-errormessage": errorId } : {}),
        })
      : children;
  return (
    <div className="min-w-0 space-y-1.5">
      <div>
        {htmlFor ? (
          <label htmlFor={htmlFor} className={labelClass}>
            {label}
          </label>
        ) : (
          <div className={labelClass}>{label}</div>
        )}
        {optional && <span className="ml-1.5 text-xs text-[var(--text-tertiary)]">optional</span>}
        {hint && (
          <p id={hintId} className="mt-0.5 text-xs leading-relaxed text-[var(--text-secondary)]">
            {hint}
          </p>
        )}
      </div>
      {control}
      {error && (
        <p id={errorId} role="alert" className="text-xs text-[var(--error)]">
          {error}
        </p>
      )}
    </div>
  );
}

// SegmentedControl is the small pill-group selector used for runtime,
// service type and similar one-of choices.
export function SegmentedControl<T extends string>({
  value,
  options,
  onChange,
  label,
}: {
  value: T | "";
  options: readonly { value: T; label: string; hint?: string }[];
  onChange: (v: T) => void;
  label: string;
}) {
  return (
    <div
      role="group"
      aria-label={label}
      className="inline-flex max-w-full flex-wrap gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5"
    >
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          title={o.hint}
          aria-pressed={value === o.value}
          onClick={() => onChange(o.value)}
          className={
            "rounded px-2.5 py-1 text-xs transition-colors " +
            (value === o.value
              ? "bg-[var(--bg-tertiary)] font-medium text-[var(--text-primary)]"
              : "text-[var(--text-secondary)] hover:text-[var(--text-primary)]")
          }
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
