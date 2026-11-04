"use client";

import { useEffect, useRef } from "react";
import { usePathname } from "next/navigation";
import {
  getServerVersionMismatch,
  onServerVersionMismatch,
  getPinnedServerVersion,
} from "@/lib/api-client";
import { toast } from "sonner";
import { editingInProgress, hasUnsavedEdits } from "@/lib/unsaved-edits";

// ServerVersionGuard reloads the SPA when the server version changes.
//
// `make ship` rolls the server pod, but the SPA chunks already loaded
// in the browser stay cached. Without an explicit signal, the user is
// running the old front-end against a new back-end — fields show up
// as "undefined", new endpoints 404, and the only fix is hard-refresh
// (which they probably won't think to do). The api-client observes
// `X-Kuso-Server-Version` on every response and flips a flag on
// drift. This component:
//
//   - Shows a one-time toast saying "Update available — reloading on
//     next nav" when the drift is first observed (so the user isn't
//     reload-spammed mid-action).
//   - Triggers `location.reload()` on the next pathname change,
//     guaranteeing a save-in-flight isn't interrupted.
//   - Falls back to a 30s timer so a user who's parked on one page
//     for a long time eventually picks up the new bundle without
//     having to navigate. The timer holds off while any form reports
//     unsaved edits (lib/unsaved-edits) or a text field has focus.
export function ServerVersionGuard() {
  const pathname = usePathname();
  const initialPath = useRef(pathname);
  const armed = useRef(false);

  useEffect(() => {
    let idleTimer: number | undefined;
    const arm = () => {
      if (armed.current) return;
      armed.current = true;
      const pinned = getPinnedServerVersion();
      toast.message("kuso updated", {
        description: pinned
          ? `Server is on a newer version than this tab (was ${pinned}). Reload to pick up new features.`
          : "Server has a newer version. Reload to pick up new features.",
        // Stays up: with unsaved edits the auto-reload below holds off
        // and this button is the only way forward.
        duration: Infinity,
        action: {
          label: "Reload",
          onClick: () => window.location.reload(),
        },
      });
      // Auto-reload on long-idle (30s after the drift is OBSERVED, not
      // after mount — a mount-time timer only covered tabs younger than
      // 30s and never fired for a tab parked open across a server roll)
      // so a tab left on the dashboard eventually catches up without
      // user action. The route-change effect below usually wins first.
      // Never while a form holds unsaved edits or a field has focus:
      // check again later and leave the toast's Reload button to the user.
      const tryReload = () => {
        if (!getServerVersionMismatch()) return;
        if (hasUnsavedEdits() || editingInProgress()) {
          idleTimer = window.setTimeout(tryReload, 30_000);
          return;
        }
        window.location.reload();
      };
      idleTimer = window.setTimeout(tryReload, 30_000);
    };
    if (getServerVersionMismatch()) arm();
    const off = onServerVersionMismatch(arm);

    return () => {
      off();
      if (idleTimer !== undefined) window.clearTimeout(idleTimer);
    };
  }, []);

  useEffect(() => {
    if (pathname === initialPath.current) return;
    if (getServerVersionMismatch()) {
      window.location.reload();
    }
  }, [pathname]);

  return null;
}
