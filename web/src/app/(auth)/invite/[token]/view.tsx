"use client";

// /invite/<token> — the public landing for an invitation link.
// Two redemption paths:
//   1. GitHub OAuth → bounce to /api/invites/redeem/oauth/start?token=…
//      which sets the invite cookie and forwards to /api/auth/github.
//      The OAuth callback consumes the cookie + attaches the new
//      user to the configured group.
//   2. Local username + password → POST /api/invites/redeem; server
//      creates the User row, attaches to the group, returns a JWT.

import { useEffect, useState } from "react";
import { useParams, useRouter } from "next/navigation";
import { api, ApiError } from "@/lib/api-client";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Github } from "lucide-react";
import { toast } from "sonner";

interface InviteSummary {
  token: string;
  groupId?: string;
  groupName?: string;
  instanceRole?: string;
  expiresAt?: string;
  usesLeft: number;
  note?: string;
}

interface AuthMethods {
  local: boolean;
  github: boolean;
  oauth2: boolean;
}

export function InviteRedeemView() {
  const params = useParams<{ token: string }>();
  const router = useRouter();
  const token = params?.token ?? "";

  const [summary, setSummary] = useState<InviteSummary | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [methods, setMethods] = useState<AuthMethods | null>(null);

  // Local-signup fields. Hidden until the user picks "Sign up with
  // username + password" so the GH-only happy path stays a single
  // click.
  const [showLocal, setShowLocal] = useState(false);
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!token || token === "_") return;
    let cancelled = false;
    api<InviteSummary>(`/api/invites/lookup/${encodeURIComponent(token)}`)
      .then((s) => {
        if (!cancelled) setSummary(s);
      })
      .catch((e) => {
        if (cancelled) return;
        const msg = e instanceof ApiError ? `${e.status}: ${e.message}` : (e as Error).message;
        setError(msg);
      });
    api<AuthMethods>("/api/auth/methods")
      .then((m) => {
        if (!cancelled) setMethods(m);
      })
      .catch(() => {
        // Methods endpoint failure isn't fatal — fall back to
        // showing both options and let the user pick.
        if (!cancelled) setMethods({ local: true, github: true, oauth2: false });
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  const onLocalSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSubmitting(true);
    try {
      const res = await api<{ access_token: string }>("/api/invites/redeem", {
        method: "POST",
        body: { token, username, email, password },
      });
      // Persist the JWT the same way the login page does. setJwt
      // writes both localStorage + the kuso.JWT_TOKEN cookie so the
      // server-side middleware sees it on the next request.
      const { setJwt } = await import("@/lib/api-client");
      setJwt(res.access_token);
      toast.success("Welcome to kuso");
      router.push("/");
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : (err as Error).message;
      toast.error(msg);
    } finally {
      setSubmitting(false);
    }
  };

  if (!token || token === "_") {
    return <p className="text-sm text-[var(--text-tertiary)]">Loading…</p>;
  }

  if (error) {
    return (
      <div className="space-y-3 text-center">
        <h1 className="font-heading text-lg font-semibold">Invitation invalid</h1>
        <p className="text-sm text-[var(--text-secondary)]">{error}</p>
        <p className="text-xs text-[var(--text-tertiary)]">
          Ask the admin who sent it to mint a new link.
        </p>
      </div>
    );
  }

  if (!summary) {
    return <p className="text-sm text-[var(--text-tertiary)]">Loading invitation…</p>;
  }

  const expiresIn = summary.expiresAt
    ? new Date(summary.expiresAt).toLocaleString()
    : "never";

  return (
    <div className="space-y-5">
      <div className="space-y-1.5 text-center">
        <h1 className="font-heading text-lg font-semibold tracking-tight">
          You&apos;re invited to kuso
        </h1>
        <p className="text-xs text-[var(--text-secondary)]">
          {summary.groupName ? (
            <>
              Joining{" "}
              <span className="font-mono text-[var(--text-primary)]">
                {summary.groupName}
              </span>
              {summary.instanceRole && (
                <>
                  {" "}as{" "}
                  <span className="font-mono text-[var(--text-primary)]">
                    {summary.instanceRole}
                  </span>
                </>
              )}
            </>
          ) : (
            "Awaiting admin approval after signup"
          )}
        </p>
        <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
          {summary.usesLeft} use{summary.usesLeft === 1 ? "" : "s"} left · expires {expiresIn}
        </p>
        {summary.note && (
          <p className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-2 text-xs text-[var(--text-secondary)]">
            {summary.note}
          </p>
        )}
      </div>

      {methods?.github && (
        <a
          href={`/api/invites/redeem/oauth/start?token=${encodeURIComponent(token)}`}
          className="inline-flex w-full items-center justify-center gap-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-2 text-sm font-medium hover:bg-[var(--bg-tertiary)]"
        >
          <Github className="h-4 w-4" />
          Sign up with GitHub
        </a>
      )}

      {methods?.local && (
        <>
          <div className="flex items-center gap-2 text-[10px] text-[var(--text-tertiary)]">
            <span className="h-px flex-1 bg-[var(--border-subtle)]" />
            <span>or</span>
            <span className="h-px flex-1 bg-[var(--border-subtle)]" />
          </div>
          {showLocal ? (
            <form onSubmit={onLocalSubmit} className="space-y-3">
              <Field label="username" hint="lowercase letters/digits/dash, ≤32">
                <Input
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  className="h-9 font-mono text-sm"
                  required
                  autoFocus
                  spellCheck={false}
                />
              </Field>
              <Field label="email">
                <Input
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  className="h-9 font-mono text-sm"
                  required
                  spellCheck={false}
                />
              </Field>
              <Field label="password" hint="≥ 8 characters">
                <Input
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="h-9 font-mono text-sm"
                  required
                  minLength={8}
                />
              </Field>
              <Button type="submit" className="w-full" disabled={submitting}>
                {submitting ? "Creating account…" : "Create account"}
              </Button>
            </form>
          ) : (
            <Button
              type="button"
              variant="outline"
              className="w-full"
              onClick={() => setShowLocal(true)}
            >
              Sign up with username + password
            </Button>
          )}
        </>
      )}
    </div>
  );
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1">
      <div className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
        {label}
      </div>
      {children}
      {hint && (
        <div className="text-[10px] text-[var(--text-tertiary)]/70">{hint}</div>
      )}
    </div>
  );
}
