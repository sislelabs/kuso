"use client";

import { CopyButton as UiCopyButton } from "@/components/ui/copy-button";

// Deprecated: kept only until ServiceOverlay imports ui/copy-button
// directly; delete this file then.
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
  return (
    <UiCopyButton
      value={value}
      label={label ?? `Copy ${what.toLowerCase()}`}
      showLabel={label !== undefined}
      size={size}
      className={className}
    />
  );
}
