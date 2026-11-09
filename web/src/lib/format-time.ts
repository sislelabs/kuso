// Absolute times and schedules, always with the zone they're in, so a
// cron at "23:00" or a log window "09:00–10:00" can't be read in the
// wrong timezone. Relative times stay in lib/format.ts (relativeTime).

export interface AbsoluteTimeOptions {
  // "UTC" for things that run on server time (cron schedules); the
  // viewer's zone otherwise.
  timeZone?: "UTC" | "local";
  // Include the date (default true).
  date?: boolean;
  seconds?: boolean;
  // Append the zone name (default true). Off for the first half of a
  // range whose second half carries it.
  zone?: boolean;
}

function zoneName(d: Date, timeZone: string | undefined): string {
  if (timeZone === "UTC") return "UTC";
  const part = new Intl.DateTimeFormat("en-US", { timeZone, timeZoneName: "short" })
    .formatToParts(d)
    .find((p) => p.type === "timeZoneName");
  return part?.value ?? "";
}

// formatAbsoluteTime: "Nov 9, 23:00 UTC" / "Nov 9, 09:55 GMT+2".
export function formatAbsoluteTime(input: string | number | Date, opts: AbsoluteTimeOptions = {}): string {
  const d = input instanceof Date ? input : new Date(input);
  if (Number.isNaN(d.getTime())) return typeof input === "string" ? input : "";
  const timeZone = opts.timeZone === "UTC" ? "UTC" : undefined;
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone,
    month: opts.date === false ? undefined : "short",
    day: opts.date === false ? undefined : "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: opts.seconds ? "2-digit" : undefined,
    hourCycle: "h23",
  }).format(d);
  return opts.zone === false ? parts : `${parts} ${zoneName(d, timeZone)}`.trim();
}

// formatTimeRange: "Nov 9, 09:00 – 10:30 GMT+2", the zone named once.
export function formatTimeRange(from: string | Date, to: string | Date, opts: Omit<AbsoluteTimeOptions, "zone"> = {}): string {
  const a = new Date(from);
  const b = new Date(to);
  const day = new Intl.DateTimeFormat("en-US", { timeZone: opts.timeZone === "UTC" ? "UTC" : undefined, dateStyle: "short" });
  const sameDay = day.format(a) === day.format(b);
  // The end repeats the date only when the range crosses midnight.
  const endDate = opts.date === false ? false : !sameDay;
  return `${formatAbsoluteTime(a, { ...opts, zone: false })} – ${formatAbsoluteTime(b, { ...opts, date: endDate })}`;
}

const DAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

function ordinal(n: number): string {
  const s = n % 100 >= 11 && n % 100 <= 13 ? "th" : (["th", "st", "nd", "rd"][n % 10] ?? "th");
  return `${n}${s}`;
}

// describeCron turns a 5-field cron (kuso schedules run in UTC) into
// words: "Daily at 23:00 UTC", "Mondays at 09:30 UTC", "Every 15 minutes".
// Shapes it doesn't recognise come back as the expression plus "(UTC)".
export function describeCron(expr: string): string {
  const s = expr.trim();
  if (!s) return "—";
  const f = s.split(/\s+/);
  if (f.length !== 5) return s;
  const [mn, hr, dom, mo, dow] = f;
  const int = (v: string, lo: number, hi: number) => /^\d+$/.test(v) && +v >= lo && +v <= hi;
  const pad = (v: string) => v.padStart(2, "0");
  const at = () => `${pad(hr)}:${pad(mn)} UTC`;
  const step = (v: string) => /^\*\/(\d+)$/.exec(v)?.[1];

  if (mo !== "*") return `${s} (UTC)`;
  if (step(mn) && hr === "*" && dom === "*" && dow === "*") {
    const n = +step(mn)!;
    return n === 1 ? "Every minute" : `Every ${n} minutes`;
  }
  if (mn === "*" && hr === "*" && dom === "*" && dow === "*") return "Every minute";
  if (int(mn, 0, 59) && hr === "*" && dom === "*" && dow === "*") return `Hourly at :${pad(mn)}`;
  if (int(mn, 0, 59) && step(hr) && dom === "*" && dow === "*") {
    return `Every ${step(hr)} hours at :${pad(mn)} (UTC)`;
  }
  if (!int(mn, 0, 59) || !int(hr, 0, 23)) return `${s} (UTC)`;
  if (dom === "*" && dow === "*") return `Daily at ${at()}`;
  if (dom === "*" && (dow === "1-5" || dow === "MON-FRI")) return `Weekdays at ${at()}`;
  if (dom === "*" && (dow === "0,6" || dow === "6,0")) return `Weekends at ${at()}`;
  if (dom === "*" && int(dow, 0, 7)) return `${DAYS[+dow % 7]}s at ${at()}`;
  if (int(dom, 1, 31) && dow === "*") return `Monthly on the ${ordinal(+dom)} at ${at()}`;
  return `${s} (UTC)`;
}
