"use client";

import { Toaster } from "sonner";
import { useTheme } from "next-themes";

// Sonner ships with a fixed-white default toast that ignored our
// theme. Wrapping it forwards the active theme + maps the toast
// surfaces to our CSS tokens so toasts pick up --bg-elevated /
// --foreground / --border-default and the orange accent for
// success/error/info pills. Same component handles light + dark.
//
// The unstyled prop tells Sonner to skip its built-in CSS so our
// classes win — otherwise sonner's white-on-shadow look layers
// over ours and you get the white pill the user reported.
export function ThemedToaster() {
  const { resolvedTheme } = useTheme();
  return (
    <Toaster
      position="bottom-right"
      theme={(resolvedTheme === "light" ? "light" : "dark") as "light" | "dark"}
      richColors
      toastOptions={{
        // Token-driven so a future palette tweak doesn't need a
        // toast-specific edit. cards/popovers share --popover, so
        // toasts riding the same surface keeps the visual family.
        style: {
          background: "var(--popover)",
          color: "var(--popover-foreground)",
          border: "1px solid var(--border-default)",
        },
        classNames: {
          // Bullet/icon colors aligned with the status hue tokens
          // exposed in globals.css so success=sage, error=red,
          // warning=orange — same vocabulary the badges use.
          success: "[&_[data-icon]]:text-[var(--success)]",
          error: "[&_[data-icon]]:text-[var(--error)]",
          warning: "[&_[data-icon]]:text-[var(--warning)]",
          info: "[&_[data-icon]]:text-[var(--info)]",
        },
      }}
    />
  );
}
