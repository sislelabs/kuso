"use client";

import { AnsiText } from "./AnsiText";
import { parseJsonLine } from "./logParse";

const JSON_SKIP = new Set(["time", "timestamp", "ts", "@timestamp", "level", "lvl", "severity", "msg", "message"]);

// Structured lines read as "message  key=value key=value" instead of a
// wall of JSON; the detail panel still shows the raw line.
export function JsonSummary({ json, highlight }: { json: Record<string, unknown>; highlight?: RegExp | null }) {
  const msg = json.msg ?? json.message;
  const fields = Object.entries(json).filter(([k]) => !JSON_SKIP.has(k));
  return (
    <>
      {typeof msg === "string" && <AnsiText text={msg} highlight={highlight} />}
      {fields.map(([k, v]) => (
        <span key={k} className="ml-2.5 inline-block text-[var(--text-tertiary)]">
          {k}=
          <span className="text-[var(--text-secondary)]">
            <AnsiText text={typeof v === "string" ? v : JSON.stringify(v)} highlight={highlight} />
          </span>
        </span>
      ))}
    </>
  );
}

// LogLineText renders one raw log line: JSON summarised, else ANSI.
export function LogLineText({ line, highlight }: { line: string; highlight?: RegExp | null }) {
  const json = parseJsonLine(line);
  return json ? <JsonSummary json={json} highlight={highlight} /> : <AnsiText text={line} highlight={highlight} />;
}
