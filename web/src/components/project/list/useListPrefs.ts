"use client";

import { useCallback, useEffect, useState } from "react";
import type { StatusFilter } from "./projectCardModel";

export type ProjectSort = "name" | "deployed";

export interface ListPrefs {
  status: StatusFilter;
  sort: ProjectSort;
}

const KEY = "kuso.projects.list";
const DEFAULTS: ListPrefs = { status: "all", sort: "name" };
const STATUSES: ReadonlyArray<StatusFilter> = ["all", "live", "failing", "building", "sleeping", "stopped"];

function parse(raw: string | null): ListPrefs {
  if (!raw) return DEFAULTS;
  try {
    const v: unknown = JSON.parse(raw);
    if (typeof v !== "object" || v === null) return DEFAULTS;
    const o = v as Record<string, unknown>;
    const status = STATUSES.find((s) => s === o.status) ?? DEFAULTS.status;
    const sort: ProjectSort = o.sort === "deployed" ? "deployed" : "name";
    return { status, sort };
  } catch {
    return DEFAULTS;
  }
}

// Per-browser filter + sort memory. Read after mount so the static-export
// prerender and the first client render agree.
export function useListPrefs(): [ListPrefs, (patch: Partial<ListPrefs>) => void] {
  const [prefs, setPrefs] = useState<ListPrefs>(DEFAULTS);
  useEffect(() => {
    try {
      setPrefs(parse(window.localStorage.getItem(KEY)));
    } catch {
      // Storage blocked: defaults are fine.
    }
  }, []);
  const update = useCallback((patch: Partial<ListPrefs>) => {
    setPrefs((prev) => {
      const next = { ...prev, ...patch };
      try {
        window.localStorage.setItem(KEY, JSON.stringify(next));
      } catch {
        // Not persisted; still applied for this visit.
      }
      return next;
    });
  }, []);
  return [prefs, update];
}

const DISMISS_KEY = "kuso.gettingStarted.dismissed";

export function useGettingStartedDismissed(): [boolean, () => void] {
  // Start dismissed so the card never flashes in before storage is read.
  const [dismissed, setDismissed] = useState(true);
  useEffect(() => {
    try {
      setDismissed(window.localStorage.getItem(DISMISS_KEY) === "1");
    } catch {
      setDismissed(false);
    }
  }, []);
  const dismiss = useCallback(() => {
    setDismissed(true);
    try {
      window.localStorage.setItem(DISMISS_KEY, "1");
    } catch {
      // Hidden for this visit only.
    }
  }, []);
  return [dismissed, dismiss];
}
