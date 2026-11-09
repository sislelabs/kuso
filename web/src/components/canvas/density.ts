"use client";

import { useCallback, useEffect, useState } from "react";

// Canvas card density, remembered per project. Compact cards drop the URL
// row so a big project fits on one screen.
export type CanvasDensity = "comfortable" | "compact";

const key = (project: string) => `kuso.canvas.density.${project}`;

function load(project: string): CanvasDensity {
  try {
    return window.localStorage.getItem(key(project)) === "compact" ? "compact" : "comfortable";
  } catch {
    return "comfortable";
  }
}

export function useCanvasDensity(project: string): [CanvasDensity, (d: CanvasDensity) => void] {
  const [density, setDensity] = useState<CanvasDensity>("comfortable");
  // Read after mount so the static HTML and the first client render agree.
  useEffect(() => {
    setDensity(load(project));
  }, [project]);
  const set = useCallback(
    (d: CanvasDensity) => {
      setDensity(d);
      try {
        window.localStorage.setItem(key(project), d);
      } catch {
        // Storage blocked: the choice lasts until reload.
      }
    },
    [project],
  );
  return [density, set];
}
