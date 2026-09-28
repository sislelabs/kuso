"use client";

import { useEffect, useState } from "react";

// Mirrors defaultHost in server-go/internal/projects/services_ops.go.
// Keep the two in step: the server decides the real host, this only
// predicts it for previews in the UI.
export function defaultServiceHost(
  service: string,
  project: string,
  projectBaseDomain: string,
  instanceDomain: string
): string {
  const base = trimDots(projectBaseDomain);
  if (base === "") {
    const inst = trimDots(instanceDomain);
    if (service === project) return `${project}.${inst}`;
    return `${service}.${project}.${inst}`;
  }
  if (service === project) return base;
  return `${service}.${base}`;
}

function trimDots(s: string): string {
  return s.trim().replace(/^\.+|\.+$/g, "");
}

// The server's fallback base is KUSO_DOMAIN, which the installer also
// uses as the UI's hostname, so the page's own host is the best guess
// the browser has. Empty until mounted (static export pre-render).
export function useInstanceDomain(): string {
  const [host, setHost] = useState("");
  useEffect(() => {
    setHost(window.location.hostname);
  }, []);
  return host;
}
