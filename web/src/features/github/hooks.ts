"use client";

import { useMutation, useQuery } from "@tanstack/react-query";
import {
  configureGithub,
  detectRuntime,
  getInstallURL,
  getSetupStatus,
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

// useSetupStatus polls the /api/github/setup-status endpoint to drive
// the /settings/github wizard. 30s staleTime so the page can re-check
// after a successful configure (it'll change from configured:false to
// configured:true once the pod restart finishes).
export function useSetupStatus() {
  return useQuery({
    queryKey: ["github", "setup-status"] as const,
    queryFn: getSetupStatus,
    staleTime: 30_000,
    refetchOnWindowFocus: false,
  });
}

export function useConfigureGithub() {
  return useMutation({ mutationFn: configureGithub });
}
