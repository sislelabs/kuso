import { ShieldAlert } from "lucide-react";
import { SEVERITY_TONE } from "./SeverityBadge";
import { cn } from "@/lib/utils";

// Canvas-node marker: the live image has fixable critical CVEs.
export function VulnNodeBadge({ count }: { count: number | undefined }) {
  if (!count) return null;
  const tone = SEVERITY_TONE.CRITICAL;
  const label = `${count} fixable critical ${count === 1 ? "vulnerability" : "vulnerabilities"}`;
  return (
    <span
      title={label}
      aria-label={label}
      className={cn(
        "inline-flex shrink-0 items-center gap-0.5 rounded border px-1 py-px font-mono text-[10px] font-semibold",
        tone.text,
        tone.bg,
        tone.border,
      )}
    >
      <ShieldAlert className="h-3 w-3" />
      {count}
    </span>
  );
}
