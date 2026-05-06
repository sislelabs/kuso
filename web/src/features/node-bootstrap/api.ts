// Web client for the v0.10 pull-mode node-join surface. Mirrors
// server-go/internal/http/handlers/node_bootstrap.go.

import { api } from "@/lib/api-client";

export interface MintTokenRequest {
  labels?: Record<string, string>;
  nodeName?: string;
  ttlSeconds?: number;
}

// MintedToken carries the cleartext jti exactly once at mint time.
// jtiPrefix is the safe-to-display 8-char head used to revoke later;
// `jti` itself is never returned again from the server (the row stores
// only sha256(jti)). Save the oneLiner before navigating away.
export interface MintedToken {
  jti: string;
  jtiPrefix: string;
  expiresAt: string;
  oneLiner: string;
  labels?: Record<string, string>;
  nodeName?: string;
}

// PendingToken is the redacted view shown in the pending-tokens list.
// jtiHash is the revoke handle (full sha256 hex); jtiPrefix is what
// the UI shows in the table so an operator can match the pending row
// against the cleartext one-liner they captured at mint time.
export interface PendingToken {
  jtiPrefix: string;
  jtiHash: string;
  createdAt: string;
  expiresAt: string;
  labels?: Record<string, string>;
  nodeName?: string;
  createdBy?: string;
}

export async function mintBootstrapToken(req: MintTokenRequest): Promise<MintedToken> {
  return api<MintedToken>("/api/kubernetes/nodes/bootstrap-tokens", {
    method: "POST",
    body: req,
  });
}

export async function listPendingBootstrapTokens(): Promise<{ tokens: PendingToken[] }> {
  return api<{ tokens: PendingToken[] }>("/api/kubernetes/nodes/bootstrap-tokens");
}

// Revoke takes the full hash (returned in the pending list) — the
// cleartext jti can no longer be used as a handle because we don't
// surface it after mint. The server also accepts a longer-than-8-char
// prefix and 409s on ambiguity.
export async function revokeBootstrapToken(jtiHash: string): Promise<void> {
  return api<void>(`/api/kubernetes/nodes/bootstrap-tokens/${encodeURIComponent(jtiHash)}`, {
    method: "DELETE",
  });
}
