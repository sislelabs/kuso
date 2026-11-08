"use client";

import { useEffect, useRef, useState } from "react";
import { Check, Copy } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";

// CopyButton copies `value` and swaps to a check for a moment. Icon-only by
// default; pass `label` for a text button. Stream S2's stand-in until the
// shared ui/copy-button lands.
export function CopyButton({
  value,
  label,
  what = "Value",
  className,
  size = "sm",
}: {
  value: string;
  label?: string;
  what?: string;
  className?: string;
  size?: "xs" | "sm";
}) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
  }, []);
  const onCopy = async (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error("Clipboard unavailable");
    }
  };
  const icon = size === "xs" ? "h-3 w-3" : "h-3.5 w-3.5";
  return (
    <button
      type="button"
      onClick={onCopy}
      aria-label={copied ? `${what} copied` : `Copy ${what.toLowerCase()}`}
      title={copied ? "Copied" : `Copy ${what.toLowerCase()}`}
      className={cn(
        "inline-flex shrink-0 items-center justify-center gap-1 rounded text-[var(--text-tertiary)] transition-colors hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)]",
        label ? "h-7 px-2 text-xs" : size === "xs" ? "h-5 w-5" : "h-6 w-6",
        className,
      )}
    >
      {copied ? <Check className={cn(icon, "text-[var(--success)]")} /> : <Copy className={icon} />}
      {label && <span>{copied ? "Copied" : label}</span>}
      <span className="sr-only" aria-live="polite">
        {copied ? "Copied" : ""}
      </span>
    </button>
  );
}
