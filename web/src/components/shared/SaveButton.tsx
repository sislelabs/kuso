"use client";

import { Button, type ButtonProps } from "@/components/ui/button";

// SaveButton is the one Save for edit-in-place forms: disabled until
// something changed, "Saving…" while the request runs, and a reason on
// hover/focus whenever it can't be pressed. `blockedReason` covers the
// other blockers (no permission, invalid field), and wins over "no
// changes" because it's the one the user has to act on.
export function SaveButton({
  dirty,
  saving = false,
  blockedReason,
  label = "Save",
  size = "sm",
  ...props
}: Omit<ButtonProps, "disabled" | "disabledReason" | "loading" | "children"> & {
  dirty: boolean;
  saving?: boolean;
  blockedReason?: string;
  label?: string;
}) {
  const reason = saving ? undefined : (blockedReason ?? (dirty ? undefined : "No changes to save"));
  return (
    <Button size={size} disabled={saving || !!reason} disabledReason={reason} {...props}>
      {saving ? "Saving…" : label}
    </Button>
  );
}
