"use client";

import {
  MutationCache,
  QueryCache,
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { useState } from "react";
import { clearJwt } from "./api-client";

// onAuthError centralises the "session expired" handling. Triggered
// from BOTH the QueryCache and MutationCache so a 401 from any path
// (background refetch, button click, dropdown menu mount) lands the
// user back on /login instead of leaving them stuck on a half-broken
// page or, worse, surfacing as the Next.js runtime "This page
// couldn't load" overlay when an error bubbles up to render.
// snapshotFormDrafts walks every <input>, <textarea>, and contenteditable
// in the page and stashes their values keyed on form-field path so the
// post-login bounce can restore them. Without this, a session expiry
// mid-edit (env var name typed, service description filled in, env-var
// editor mid-add) loses everything and the user has to retype.
//
// Best-effort: storage failures (quota, private mode) silently skip.
function snapshotFormDrafts() {
  if (typeof window === "undefined") return;
  try {
    const route = window.location.pathname;
    const draft: Record<string, string> = {};
    for (const el of Array.from(document.querySelectorAll<HTMLElement>("input,textarea,[contenteditable=true]"))) {
      // Skip password fields — never persist secrets through a redirect.
      if (el instanceof HTMLInputElement && el.type === "password") continue;
      const key = el.getAttribute("name") || el.id;
      if (!key) continue;
      let value: string;
      if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) {
        value = el.value;
      } else {
        value = el.textContent || "";
      }
      if (value && value.length > 0) draft[key] = value;
    }
    if (Object.keys(draft).length > 0) {
      window.sessionStorage.setItem(
        `kuso.form-draft.${route}`,
        JSON.stringify({ at: Date.now(), draft }),
      );
    }
  } catch {
    /* noop — storage failure is acceptable */
  }
}

function onAuthError(err: unknown) {
  if (!err || typeof err !== "object" || !("status" in err)) return;
  const s = (err as { status: number }).status;
  if (s !== 401) return;
  // Stash whatever the user was typing before we yank them to login.
  // restoreFormDraft on the post-login page can re-populate fields by
  // matching name/id; consumers don't have to opt in unless they want
  // the better UX.
  snapshotFormDrafts();
  // Clear the bad token and bounce to login. The AuthGate would
  // eventually do this on the next session refetch, but doing it
  // here is faster and stops cascading failures from re-firing
  // queries with the dead token.
  clearJwt();
  if (typeof window !== "undefined") {
    const next = encodeURIComponent(window.location.pathname);
    if (!window.location.pathname.startsWith("/login")) {
      window.location.replace(`/login?next=${next}`);
    }
  }
}

// restoreFormDraft is called by pages that want post-login restoration.
// Returns the draft map for the current pathname (or null) and clears
// the entry so a future refresh doesn't surprise-fill the form.
export function restoreFormDraft(): Record<string, string> | null {
  if (typeof window === "undefined") return null;
  try {
    const key = `kuso.form-draft.${window.location.pathname}`;
    const raw = window.sessionStorage.getItem(key);
    if (!raw) return null;
    window.sessionStorage.removeItem(key);
    const parsed = JSON.parse(raw) as { at: number; draft: Record<string, string> };
    // Drop drafts older than 30 minutes — if the user took that long,
    // restoring would surprise them more than help.
    if (!parsed.at || Date.now() - parsed.at > 30 * 60 * 1000) return null;
    return parsed.draft || null;
  } catch {
    return null;
  }
}

export function QueryProvider({ children }: { children: React.ReactNode }) {
  const [client] = useState(
    () =>
      new QueryClient({
        // Cache-level handlers fire for every query/mutation; a global
        // session-expiry redirect lives here so individual hooks don't
        // each have to remember to handle 401 themselves.
        queryCache: new QueryCache({ onError: onAuthError }),
        mutationCache: new MutationCache({ onError: onAuthError }),
        defaultOptions: {
          queries: {
            staleTime: 30_000,
            refetchOnWindowFocus: true,
            // throwOnError defaults to false in v5; explicit so render
            // boundaries don't accidentally catch every API hiccup.
            // Pages that want to surface errors do so by reading
            // query.isError on the return value.
            throwOnError: false,
            retry: (failureCount, err) => {
              if (err && typeof err === "object" && "status" in err) {
                const s = (err as { status: number }).status;
                if (s === 401 || s === 403 || s === 404) return false;
              }
              return failureCount < 2;
            },
          },
        },
      })
  );
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}
