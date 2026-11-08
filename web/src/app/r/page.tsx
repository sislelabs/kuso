"use client";

import { useEffect, useRef, useState } from "react";
import { ExternalLink, Check, AlertCircle, X as XIcon } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { normalizeReviewerView, type ReviewerView } from "./reviewerView";

// Public reviewer page (v0.17.0 Phase 2). Unauthenticated — the URL
// token is the only credential, so kuso login isn't required. Layout
// is deliberately minimal: a non-technical reviewer should be able
// to open the link, see "what is this", click the preview to test
// it, and submit one of three verdicts. No nav chrome, no kuso
// account prompt, no upsell.
//
// Token comes from URL hash (#<token>) instead of a path segment so
// the page can ship under `output: export` (Next.js static export
// can't pre-render dynamic [param] routes). Same UX, just hash-
// suffix instead of slash-suffix:
//   https://<kuso-domain>/r/#abc123...
//
// Backend: GET /api/reviews/<token> + POST /api/reviews/<token>/decision

export default function ReviewerPage() {
  const [token, setToken] = useState<string>("");
  const [view, setView] = useState<ReviewerView | null>(null);
  // The polling interval below is created once per token and would
  // otherwise close over the INITIAL `view` (null) forever — meaning
  // "stop once seeded/decided" never triggered and the page polled for
  // its whole lifetime. The ref always holds the latest view.
  const viewRef = useRef(view);
  viewRef.current = view;
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [comment, setComment] = useState("");
  const [reviewerEmail, setReviewerEmail] = useState("");
  const [submitting, setSubmitting] = useState(false);

  // Read token from URL hash. Falls back to "" so an empty hash
  // shows the "link not found" state instead of trying to fetch.
  useEffect(() => {
    const t = window.location.hash.replace(/^#/, "");
    // Opaque tokens only: anything else (e.g. "../projects/x") would turn
    // the fetches below into calls to other /api paths.
    setToken(/^[A-Za-z0-9_-]+$/.test(t) ? t : "");
  }, []);

  useEffect(() => {
    if (!token) {
      setLoading(false);
      return;
    }
    void fetchReview();
    // Re-fetch every 10s while seedPhase != succeeded so the reviewer
    // sees "seeding…" flip to "ready" automatically. Once the
    // decision is recorded the page stops polling.
    const id = setInterval(() => {
      const v = viewRef.current;
      if (!v || (v.seedPhase !== "succeeded" && v.decision === "")) {
        void fetchReview();
      }
    }, 10000);
    return () => clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token]);

  async function fetchReview() {
    if (!token) return;
    try {
      const res = await fetch(`/api/reviews/${encodeURIComponent(token)}`, { credentials: "omit" });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      setView(normalizeReviewerView(await res.json()));
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "load failed");
    } finally {
      setLoading(false);
    }
  }

  async function submit(decision: "approved" | "changes_requested" | "denied") {
    setSubmitting(true);
    try {
      const res = await fetch(`/api/reviews/${encodeURIComponent(token)}/decision`, {
        method: "POST",
        // The token is the credential; never ride the operator's session.
        credentials: "omit",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          decision,
          comment,
          reviewer: reviewerEmail.trim(),
        }),
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      setView(normalizeReviewerView(await res.json()));
    } catch (e) {
      toast.error(e instanceof Error ? `Couldn't submit: ${e.message}` : "Couldn't submit");
    } finally {
      setSubmitting(false);
    }
  }

  if (loading) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[var(--bg-primary)] text-[var(--text-secondary)]">
        Loading…
      </div>
    );
  }
  if (!token || error || !view) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[var(--bg-primary)] text-[var(--text-secondary)]">
        <div className="max-w-md text-center">
          <h1 className="mb-2 text-xl font-semibold text-[var(--text-primary)]">Link not found</h1>
          <p className="text-sm">
            This reviewer URL doesn&apos;t exist or has expired. Ask the developer
            for a fresh link.
          </p>
        </div>
      </div>
    );
  }

  const decided = view.decision !== "";
  const seedFailed = view.seedPhase === "failed";
  const seedRunning = view.seedPhase === "running" || view.seedPhase === "pending";

  return (
    <div className="min-h-screen bg-[var(--bg-primary)] text-[var(--text-primary)]">
      <div className="mx-auto max-w-3xl px-6 py-12">
        {/* Header */}
        <div className="mb-8 border-b border-[var(--border-subtle)] pb-6">
          <p className="mb-2 text-xs font-medium text-[var(--text-secondary)]">
            {view.project} · PR #{view.prNumber}
          </p>
          <h1 className="text-title text-2xl">{view.prTitle || `PR #${view.prNumber}`}</h1>
          {view.prAuthor && (
            <p className="mt-2 text-sm text-[var(--text-secondary)]">
              Submitted by {view.prAuthor} (<span className="font-mono">{view.headRef}</span> → <span className="font-mono">{view.baseRef}</span>)
            </p>
          )}
          {view.prBody && (
            <pre className="mt-4 max-h-40 overflow-auto whitespace-pre-wrap rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-3 font-mono text-xs text-[var(--text-secondary)]">
              {view.prBody}
            </pre>
          )}
        </div>

        {/* Seed status banner */}
        {seedRunning && (
          <div className="mb-6 rounded-md border border-[var(--info)]/30 bg-[var(--info-subtle)] px-4 py-3 text-sm text-[var(--info)]">
            Setting up the preview…
            {view.seedPhase === "running" && " (running seed script)"}
          </div>
        )}
        {seedFailed && (
          <div className="mb-6 rounded-md border border-[var(--danger)]/30 bg-[var(--danger-subtle)] px-4 py-3 text-sm text-[var(--danger)]">
            <strong>Preview seed failed.</strong> The app may not show realistic
            data until the developer fixes it.
            {view.seedError && (
              <pre className="mt-2 max-h-32 overflow-auto whitespace-pre-wrap font-mono text-xs text-[var(--text-secondary)]">
                {view.seedError}
              </pre>
            )}
          </div>
        )}

        {/* Preview URLs */}
        <section className="mb-8">
          <h2 className="mb-3 font-semibold">Open the preview</h2>
          {view.services.length === 0 ? (
            <p className="text-sm text-[var(--text-secondary)]">
              No preview URLs are ready yet. Refresh in a moment.
            </p>
          ) : (
            <div className="space-y-2">
              {view.services.map((svc) => (
                <a
                  key={svc.service}
                  href={svc.url}
                  target="_blank"
                  rel="noreferrer"
                  className="flex items-center justify-between min-h-11 rounded-md border border-[var(--border-default)] bg-[var(--bg-secondary)] px-4 py-3 text-sm transition-colors hover:border-[var(--border-strong)] hover:bg-[var(--bg-tertiary)] focus-visible:ring-2 focus-visible:ring-[var(--focus)] focus-visible:outline-none"
                >
                  <span className="min-w-0 truncate font-mono text-[var(--link)]">{svc.url}</span>
                  <ExternalLink className="h-4 w-4 shrink-0 text-[var(--text-tertiary)]" />
                </a>
              ))}
            </div>
          )}
        </section>

        {/* Decision */}
        {decided ? (
          <section className="rounded-md border border-[var(--border-default)] bg-[var(--bg-secondary)] px-4 py-5">
            <div className="mb-2 flex items-center gap-2 font-semibold">
              {view.decision === "approved" && <Check className="h-4 w-4 text-[var(--success)]" />}
              {view.decision === "changes_requested" && <AlertCircle className="h-4 w-4 text-[var(--warning)]" />}
              {view.decision === "denied" && <XIcon className="h-4 w-4 text-[var(--danger)]" />}
              {view.decision === "approved" && "Approved"}
              {view.decision === "changes_requested" && "Changes requested"}
              {view.decision === "denied" && "Denied"}
            </div>
            {view.decisionComment && (
              <p className="whitespace-pre-wrap text-sm text-[var(--text-secondary)]">
                {view.decisionComment}
              </p>
            )}
            <p className="mt-3 text-xs text-[var(--text-tertiary)]">
              By {view.decidedBy || "anonymous"}{" "}
              {view.decidedAt && (
                <>· {new Date(view.decidedAt).toLocaleString()}</>
              )}
            </p>
            <p className="mt-4 text-xs text-[var(--text-tertiary)]">
              You can resubmit by reloading and choosing again.
            </p>
          </section>
        ) : (
          <section>
            <h2 className="mb-3 font-semibold">Leave a decision</h2>
            <div className="mb-3 grid grid-cols-1 gap-2 sm:grid-cols-2">
              <Input
                type="email"
                placeholder="Your email (optional)"
                value={reviewerEmail}
                onChange={(e) => setReviewerEmail(e.target.value)}
                aria-label="Your email"
              />
            </div>
            <textarea
              placeholder="Comment (optional but helpful)…"
              value={comment}
              onChange={(e) => setComment(e.target.value)}
              rows={5}
              aria-label="Comment"
              className="mb-3 w-full rounded-md border border-[var(--border-default)] bg-input px-3 py-2 text-sm text-[var(--text-primary)] outline-none hover:border-[var(--border-strong)] focus-visible:border-[var(--focus)] focus-visible:ring-3 focus-visible:ring-[var(--focus)]/25"
            />
            <div className="flex flex-col gap-2 sm:flex-row">
              <Button size="lg" className="flex-1" onClick={() => submit("approved")} disabled={submitting}>
                <Check aria-hidden /> Approve
              </Button>
              <Button
                size="lg"
                variant="secondary"
                className="flex-1"
                onClick={() => submit("changes_requested")}
                disabled={submitting}
              >
                <AlertCircle aria-hidden /> Request changes
              </Button>
              <Button
                size="lg"
                variant="destructive-subtle"
                className="flex-1"
                onClick={() => submit("denied")}
                disabled={submitting}
              >
                <XIcon aria-hidden /> Deny
              </Button>
            </div>
          </section>
        )}

        <p className="mt-12 text-center text-xs text-[var(--text-tertiary)]">
          Powered by kuso · auto-generated reviewer page
        </p>
      </div>
    </div>
  );
}
