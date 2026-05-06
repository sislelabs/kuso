"use client";

import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";

import {
  listPendingBootstrapTokens,
  mintBootstrapToken,
  revokeBootstrapToken,
  type MintTokenRequest,
  type MintedToken,
  type PendingToken,
} from "./api";

export const pendingBootstrapTokensKey = ["node-bootstrap", "pending"] as const;

// usePendingBootstrapTokens polls every 5s while the AddNodeModal is
// open. The server returns at most 200 rows so the response stays
// small; the 5s cadence is fast enough that the operator sees the
// "joined" transition almost immediately when the new VM phones home.
export function usePendingBootstrapTokens(opts?: { enabled?: boolean; intervalMs?: number }) {
  return useQuery({
    queryKey: pendingBootstrapTokensKey,
    queryFn: () => listPendingBootstrapTokens(),
    enabled: opts?.enabled ?? true,
    refetchInterval: opts?.intervalMs ?? 5_000,
    refetchIntervalInBackground: false,
  });
}

export function useMintBootstrapToken() {
  const qc = useQueryClient();
  return useMutation<MintedToken, Error, MintTokenRequest>({
    mutationFn: mintBootstrapToken,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: pendingBootstrapTokensKey });
    },
  });
}

export function useRevokeBootstrapToken() {
  const qc = useQueryClient();
  return useMutation<void, Error, string>({
    mutationFn: (jti) => revokeBootstrapToken(jti),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: pendingBootstrapTokensKey });
    },
  });
}

export type { MintedToken, PendingToken, MintTokenRequest };
