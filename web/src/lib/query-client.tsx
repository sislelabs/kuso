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
function onAuthError(err: unknown) {
  if (!err || typeof err !== "object" || !("status" in err)) return;
  const s = (err as { status: number }).status;
  if (s !== 401) return;
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
