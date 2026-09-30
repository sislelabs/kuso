"use client";

import { useEffect } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  configureGithub,
  detectRuntime,
  getInstallURL,
  getSetupStatus,
  listInstallationRepos,
  listInstallations,
  listRepos,
  scanAddons,
} from "./api";

// The /github/installed landing tab posts here so the tab that started
// the install refetches its repo list without a manual reload.
export const GITHUB_INSTALLED_CHANNEL = "kuso-github-installed";

export function announceGithubInstalled() {
  if (typeof BroadcastChannel === "undefined") return;
  const ch = new BroadcastChannel(GITHUB_INSTALLED_CHANNEL);
  ch.postMessage("installed");
  ch.close();
}

function useRefetchOnGithubInstalled() {
  const qc = useQueryClient();
  useEffect(() => {
    if (typeof BroadcastChannel === "undefined") return;
    const ch = new BroadcastChannel(GITHUB_INSTALLED_CHANNEL);
    ch.onmessage = () => {
      void qc.invalidateQueries({ queryKey: ["github"] });
    };
    return () => ch.close();
  }, [qc]);
}

export function useInstallURL() {
  return useQuery({ queryKey: ["github", "install-url"] as const, queryFn: getInstallURL, staleTime: 60_000 });
}

export function useInstallations(opts: { enabled?: boolean } = {}) {
  useRefetchOnGithubInstalled();
  // "always": the usual flow is install on github.com, then switch back
  // to this tab well inside staleTime, and the new install must show.
  return useQuery({
    enabled: opts.enabled ?? true,
    queryKey: ["github", "installations"] as const,
    queryFn: listInstallations,
    staleTime: 60_000,
    refetchOnWindowFocus: "always",
  });
}

// useGithubRepos is the non-admin repo picker source. 403 means the
// caller lacks projects:create — surfaced by the caller, not toasted.
export function useGithubRepos(opts: { enabled?: boolean } = {}) {
  useRefetchOnGithubInstalled();
  return useQuery({
    queryKey: ["github", "repos"] as const,
    queryFn: listRepos,
    enabled: opts.enabled ?? true,
    staleTime: 60_000,
    refetchOnWindowFocus: "always",
  });
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
  // Callers swallow errors deliberately (.catch → leave defaults), so
  // the global error toast must not fire for a failed runtime detect.
  return useMutation({ meta: { skipGlobalErrorToast: true }, mutationFn: detectRuntime });
}

export function useScanAddons() {
  // Suggestions are a nice-to-have; a failed scan must not toast.
  return useMutation({ meta: { skipGlobalErrorToast: true }, mutationFn: scanAddons });
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
  // The github settings page mutateAsyncs this in a try/catch and toasts
  // the failure itself — opt out of the global toast to avoid doubling.
  return useMutation({ meta: { skipGlobalErrorToast: true }, mutationFn: configureGithub });
}
