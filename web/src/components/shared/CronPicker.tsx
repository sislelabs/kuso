"use client";

import { useState, useEffect } from "react";

// CronPicker — friendly schedule editor that emits a 5-field UTC cron
// expression. Replaces the textbox-with-cheatsheet pattern that
// scattered across BackupsTab + ServiceCronsPanel + the new Add-cron
// flow. Modes:
//
//   hourly  → "MM * * * *"             (every hour at MM past)
//   daily   → "MM HH * * *"            (every day at HH:MM UTC)
//   weekly  → "MM HH * * D"            (one day-of-week at HH:MM)
//   monthly → "MM HH N * *"            (day-of-month N at HH:MM)
//   custom  → free-text 5-field cron   (escape hatch for power users)
//
// All times are UTC because every k8s CronJob runs in UTC by default
// and we don't want to mislead users into thinking "08:00" means
// their local time. Hint text + label make this explicit.
//
// parseSchedule is best-effort: an inbound "0 3 * * *" recognises as
// daily-at-03:00; anything that doesn't match a preset shape lands
// in custom mode showing the literal cron string.

export interface CronPickerProps {
  value: string;
  onChange: (cron: string) => void;
  // disabled hides the inputs but keeps the value (useful when a
  // parent has its own enable/disable switch).
  disabled?: boolean;
  // hideCustom drops the "Custom" tab entirely. Useful when the
  // surrounding form needs to constrain users to the friendly modes.
  hideCustom?: boolean;
}

type Mode = "hourly" | "daily" | "weekly" | "monthly" | "custom";

interface Parsed {
  mode: Mode;
  minute: number;
  hour: number;
  // Day of week (0-6, Sunday=0) for weekly mode.
  dow: number;
  // Day of month (1-28 only — anything higher skips months) for
  // monthly mode.
  dom: number;
}

const DEFAULT_PARSED: Parsed = {
  mode: "daily",
  minute: 0,
  hour: 3,
  dow: 1,
  dom: 1,
};

// parseSchedule attempts to recognise a preset shape. Returns
// {mode:"custom"} for anything we can't decompose so the user keeps
// editing the literal text rather than having it silently rewritten.
function parseSchedule(cron: string): Parsed {
  const s = cron.trim();
  if (!s) return DEFAULT_PARSED;
  const fields = s.split(/\s+/);
  if (fields.length !== 5) return { ...DEFAULT_PARSED, mode: "custom" };
  const [mn, hr, dom, mo, dow] = fields;
  const isInt = (v: string, lo: number, hi: number) => {
    if (!/^\d+$/.test(v)) return false;
    const n = parseInt(v, 10);
    return n >= lo && n <= hi;
  };
  // hourly: "MM * * * *"
  if (isInt(mn, 0, 59) && hr === "*" && dom === "*" && mo === "*" && dow === "*") {
    return { ...DEFAULT_PARSED, mode: "hourly", minute: parseInt(mn, 10) };
  }
  // daily: "MM HH * * *"
  if (
    isInt(mn, 0, 59) &&
    isInt(hr, 0, 23) &&
    dom === "*" &&
    mo === "*" &&
    dow === "*"
  ) {
    return {
      ...DEFAULT_PARSED,
      mode: "daily",
      minute: parseInt(mn, 10),
      hour: parseInt(hr, 10),
    };
  }
  // weekly: "MM HH * * D"
  if (
    isInt(mn, 0, 59) &&
    isInt(hr, 0, 23) &&
    dom === "*" &&
    mo === "*" &&
    isInt(dow, 0, 6)
  ) {
    return {
      mode: "weekly",
      minute: parseInt(mn, 10),
      hour: parseInt(hr, 10),
      dow: parseInt(dow, 10),
      dom: 1,
    };
  }
  // monthly: "MM HH N * *"
  if (
    isInt(mn, 0, 59) &&
    isInt(hr, 0, 23) &&
    isInt(dom, 1, 28) &&
    mo === "*" &&
    dow === "*"
  ) {
    return {
      mode: "monthly",
      minute: parseInt(mn, 10),
      hour: parseInt(hr, 10),
      dom: parseInt(dom, 10),
      dow: 1,
    };
  }
  return { ...DEFAULT_PARSED, mode: "custom" };
}

function compose(p: Parsed): string {
  switch (p.mode) {
    case "hourly":
      return `${p.minute} * * * *`;
    case "daily":
      return `${p.minute} ${p.hour} * * *`;
    case "weekly":
      return `${p.minute} ${p.hour} * * ${p.dow}`;
    case "monthly":
      return `${p.minute} ${p.hour} ${p.dom} * *`;
    case "custom":
      return ""; // custom mode owns its own text state; this branch isn't used
  }
}

function pad2(n: number): string {
  return n < 10 ? `0${n}` : String(n);
}

const DOW_LABELS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

export function CronPicker({
  value,
  onChange,
  disabled,
  hideCustom,
}: CronPickerProps) {
  const initialParsed = parseSchedule(value);
  const [mode, setMode] = useState<Mode>(initialParsed.mode);
  const [minute, setMinute] = useState(initialParsed.minute);
  const [hour, setHour] = useState(initialParsed.hour);
  const [dow, setDow] = useState(initialParsed.dow);
  const [dom, setDom] = useState(initialParsed.dom);
  const [custom, setCustom] = useState(
    initialParsed.mode === "custom" ? value : "",
  );

  // Re-parse when the parent value changes (e.g. baseline reset on
  // refetch). Without this the picker holds the user's stale edits.
  useEffect(() => {
    const p = parseSchedule(value);
    setMode(p.mode);
    setMinute(p.minute);
    setHour(p.hour);
    setDow(p.dow);
    setDom(p.dom);
    if (p.mode === "custom") setCustom(value);
  }, [value]);

  const emit = (next: Partial<Parsed> & { mode: Mode }) => {
    if (next.mode === "custom") {
      onChange(custom);
      return;
    }
    onChange(
      compose({
        mode: next.mode,
        minute: next.minute ?? minute,
        hour: next.hour ?? hour,
        dow: next.dow ?? dow,
        dom: next.dom ?? dom,
      }),
    );
  };

  const tabs: { key: Mode; label: string }[] = [
    { key: "hourly", label: "Hourly" },
    { key: "daily", label: "Daily" },
    { key: "weekly", label: "Weekly" },
    { key: "monthly", label: "Monthly" },
  ];
  if (!hideCustom) tabs.push({ key: "custom", label: "Custom" });

  const numberInput = (
    val: number,
    onSet: (n: number) => void,
    lo: number,
    hi: number,
    width: string,
  ) => (
    <input
      type="number"
      value={val}
      min={lo}
      max={hi}
      disabled={disabled}
      onChange={(e) => {
        const n = parseInt(e.target.value, 10);
        if (Number.isNaN(n)) return;
        const clamped = Math.max(lo, Math.min(hi, n));
        onSet(clamped);
        // Emit synchronously based on the new value rather than the
        // stale state — useState's setter doesn't batch with the
        // closure here.
        emit({
          mode,
          minute: onSet === setMinute ? clamped : minute,
          hour: onSet === setHour ? clamped : hour,
          dow: onSet === setDow ? clamped : dow,
          dom: onSet === setDom ? clamped : dom,
        });
      }}
      className={`h-7 ${width} rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 text-center font-mono text-[12px] text-[var(--text-primary)] disabled:opacity-50`}
    />
  );

  return (
    <div className="space-y-2">
      <div className="inline-flex rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5">
        {tabs.map((t) => (
          <button
            key={t.key}
            type="button"
            disabled={disabled}
            onClick={() => {
              setMode(t.key);
              if (t.key === "custom") {
                onChange(custom || value);
              } else {
                emit({ mode: t.key });
              }
            }}
            className={
              "rounded px-2 py-1 font-mono text-[11px] transition-colors " +
              (mode === t.key
                ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]")
            }
          >
            {t.label}
          </button>
        ))}
      </div>

      {mode === "hourly" && (
        <div className="flex items-center gap-1.5 font-mono text-[11px] text-[var(--text-secondary)]">
          <span>at</span>
          {numberInput(minute, setMinute, 0, 59, "w-16")}
          <span>minutes past every hour</span>
        </div>
      )}

      {mode === "daily" && (
        <div className="flex items-center gap-1.5 font-mono text-[11px] text-[var(--text-secondary)]">
          <span>every day at</span>
          {numberInput(hour, setHour, 0, 23, "w-14")}
          <span>:</span>
          {numberInput(minute, setMinute, 0, 59, "w-14")}
          <span>UTC</span>
        </div>
      )}

      {mode === "weekly" && (
        <div className="space-y-1.5">
          <div className="flex flex-wrap items-center gap-1">
            {DOW_LABELS.map((label, i) => (
              <button
                key={i}
                type="button"
                disabled={disabled}
                onClick={() => {
                  setDow(i);
                  emit({ mode: "weekly", dow: i });
                }}
                className={
                  "rounded border px-2 py-0.5 font-mono text-[10px] " +
                  (dow === i
                    ? "border-[var(--accent)]/60 bg-[var(--accent)]/10 text-[var(--accent)]"
                    : "border-[var(--border-subtle)] bg-[var(--bg-primary)] text-[var(--text-tertiary)] hover:text-[var(--text-primary)]")
                }
              >
                {label}
              </button>
            ))}
          </div>
          <div className="flex items-center gap-1.5 font-mono text-[11px] text-[var(--text-secondary)]">
            <span>at</span>
            {numberInput(hour, setHour, 0, 23, "w-14")}
            <span>:</span>
            {numberInput(minute, setMinute, 0, 59, "w-14")}
            <span>UTC</span>
          </div>
        </div>
      )}

      {mode === "monthly" && (
        <div className="flex flex-wrap items-center gap-1.5 font-mono text-[11px] text-[var(--text-secondary)]">
          <span>day</span>
          {numberInput(dom, setDom, 1, 28, "w-14")}
          <span>at</span>
          {numberInput(hour, setHour, 0, 23, "w-14")}
          <span>:</span>
          {numberInput(minute, setMinute, 0, 59, "w-14")}
          <span>UTC · max day 28 (months vary)</span>
        </div>
      )}

      {mode === "custom" && (
        <div className="space-y-1">
          <input
            type="text"
            value={custom}
            disabled={disabled}
            onChange={(e) => {
              setCustom(e.target.value);
              onChange(e.target.value);
            }}
            placeholder="e.g. 0 */6 * * *"
            spellCheck={false}
            className="h-7 w-full rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[12px] text-[var(--text-primary)] disabled:opacity-50"
          />
          <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
            Five fields: minute hour day-of-month month day-of-week. UTC.
          </p>
        </div>
      )}

      {/* Resolved cron preview — power users like to confirm what
          the picker actually emits. Kept tiny so it doesn't compete
          with the picker for attention. */}
      <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
        cron: <span className="text-[var(--text-secondary)]">{value || "(empty)"}</span>
      </p>
    </div>
  );
}
