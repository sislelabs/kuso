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
import { useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { useRouteParams } from "@/lib/dynamic-params";
import { sessionQueryKey } from "@/features/auth/hooks";
import { api, ApiError } from "@/lib/api-client";
import { Input } from "@/components/ui/input";
import { Button, buttonVariants } from "@/components/ui/button";
import { LoadingState } from "@/components/ui/loading-state";
import { Github } from "lucide-react";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";

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
  // useRouteParams reads from window.location.pathname, bypassing
  // Next's static-export "_" placeholder that useParams() would
  // otherwise hand back. Without this, /invite/<real-token> renders
  // the build-time placeholder render forever and never advances
  // past "Loading…".
  const params = useRouteParams<{ token: string }>(["token"]);
  const router = useRouter();
  const qc = useQueryClient();
  const token = params.token ?? "";

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
    if (!token) return;
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
      await api<{ access_token: string }>("/api/invites/redeem", {
        method: "POST",
        body: { token, username, email, password },
      });
      // Mirror useLogin's identity boundary: without invalidating the
      // session query, the mounted session observer still holds its
      // cached "logged out" result and bounces the freshly-created
      // account straight back to /login. clear() drops any stale cache
      // and forces the session observer to refetch with the new cookie.
      qc.clear();
      await qc.invalidateQueries({ queryKey: sessionQueryKey });
      toast.success("Welcome to kuso");
      router.replace("/");
    } catch (err) {
      toastError(err, "Couldn't create your account");
    } finally {
      setSubmitting(false);
    }
  };

  // Empty token: pre-hydration render. useRouteParams returns {}
  // until window is available, so we show the spinner once and let
  // the next render kick off the lookup.
  if (!token) {
    return <LoadingState kind="card" />;
  }

  if (error) {
    return (
      <div className="space-y-3 text-center">
        <h1 className="text-lg font-semibold">Invitation invalid</h1>
        <p className="text-sm text-[var(--text-secondary)]">{error}</p>
        <p className="text-xs text-[var(--text-tertiary)]">
          Ask the admin who sent it to mint a new link.
        </p>
      </div>
    );
  }

  if (!summary) {
    return <LoadingState kind="inline" label="loading invitation…" />;
  }

  const expiresIn = summary.expiresAt
    ? new Date(summary.expiresAt).toLocaleString()
    : "never";

  return (
    <div className="space-y-5">
      <div className="space-y-1.5 text-center">
        <h1 className="text-lg font-semibold tracking-tight">
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
        <p className="text-xs text-[var(--text-tertiary)]">
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
          className={`${buttonVariants({ variant: "secondary" })} w-full`}
        >
          <Github className="h-4 w-4" />
          Sign up with GitHub
        </a>
      )}

      {methods?.local && (
        <>
          <div className="flex items-center gap-3 text-xs text-[var(--text-tertiary)]">
            <span className="h-px flex-1 bg-[var(--border-subtle)]" />
            <span>or</span>
            <span className="h-px flex-1 bg-[var(--border-subtle)]" />
          </div>
          {showLocal ? (
            <form onSubmit={onLocalSubmit} className="space-y-3">
              <Field label="Username" hint="Lowercase letters, digits and dashes; up to 32 characters.">
                <Input
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  className="h-9 text-sm"
                  required
                  autoFocus
                  spellCheck={false}
                />
              </Field>
              <Field label="Email">
                <Input
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  className="h-9 text-sm"
                  required
                  spellCheck={false}
                />
              </Field>
              <Field label="Password" hint="At least 8 characters.">
                <Input
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="h-9 text-sm"
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
    <label className="block space-y-1.5">
      <span className="block text-sm font-medium text-[var(--text-primary)]">{label}</span>
      {children}
      {hint && <span className="block text-hint">{hint}</span>}
    </label>
  );
}
