"use client";

import { useMutation, useQuery } from "@tanstack/react-query";
import {
  detectRuntime,
  getInstallURL,
  listInstallationRepos,
  listInstallations,
  scanAddons,
} from "./api";

export function useInstallURL() {
  return useQuery({ queryKey: ["github", "install-url"] as const, queryFn: getInstallURL, staleTime: 60_000 });
}

export function useInstallations() {
  return useQuery({ queryKey: ["github", "installations"] as const, queryFn: listInstallations, staleTime: 60_000 });
}

export function useInstallationRepos(installationId: number | null) {
  return useQuery({
    queryKey: ["github", "installations", installationId, "repos"] as const,
    queryFn: () => listInstallationRepos(installationId!),
    enabled: !!installationId,
    staleTime: 60_000,
  });
}

export function useDetectRuntime() {
  return useMutation({ mutationFn: detectRuntime });
}

export function useScanAddons() {
  return useMutation({ mutationFn: scanAddons });
}
