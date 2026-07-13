"use client";

import { useEffect, useRef, useState } from "react";
import { ExternalLink, Check, AlertCircle, X as XIcon } from "lucide-react";

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

interface ReviewerView {
  project: string;
  prNumber: number;
  prTitle: string;
  prBody: string;
  prAuthor: string;
  baseRef: string;
  headRef: string;
  services: { service: string; url: string }[];
  seedPhase: string;
  seedError?: string;
  decision: string;
  decisionComment?: string;
  decidedAt?: string;
  decidedBy?: string;
  closed: boolean;
}

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
    setToken(t);
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
      const res = await fetch(`/api/reviews/${token}`);
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data: ReviewerView = await res.json();
      setView(data);
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
      const res = await fetch(`/api/reviews/${token}/decision`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          decision,
          comment,
          reviewer: reviewerEmail.trim(),
        }),
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data: ReviewerView = await res.json();
      setView(data);
    } catch (e) {
      alert(e instanceof Error ? e.message : "submit failed");
    } finally {
      setSubmitting(false);
    }
  }

  if (loading) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[#0a0a0a] text-neutral-400">
        Loading…
      </div>
    );
  }
  if (!token || error || !view) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[#0a0a0a] text-neutral-400">
        <div className="max-w-md text-center">
          <h1 className="mb-2 text-xl font-semibold text-neutral-200">Link not found</h1>
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
    <div className="min-h-screen bg-[#0a0a0a] text-neutral-200">
      <div className="mx-auto max-w-3xl px-6 py-12">
        {/* Header */}
        <div className="mb-8 border-b border-neutral-800 pb-6">
          <p className="mb-2 font-mono text-xs uppercase tracking-widest text-neutral-500">
            {view.project} · PR #{view.prNumber}
          </p>
          <h1 className="text-2xl font-semibold">{view.prTitle || `PR #${view.prNumber}`}</h1>
          {view.prAuthor && (
            <p className="mt-2 text-sm text-neutral-500">
              Submitted by {view.prAuthor} ({view.headRef} → {view.baseRef})
            </p>
          )}
          {view.prBody && (
            <pre className="mt-4 max-h-40 overflow-auto whitespace-pre-wrap rounded-md border border-neutral-800 bg-neutral-900 p-3 font-mono text-xs text-neutral-400">
              {view.prBody}
            </pre>
          )}
        </div>

        {/* Seed status banner */}
        {seedRunning && (
          <div className="mb-6 rounded-md border border-blue-500/40 bg-blue-500/10 px-4 py-3 text-sm text-blue-200">
            Setting up the preview…
            {view.seedPhase === "running" && " (running seed script)"}
          </div>
        )}
        {seedFailed && (
          <div className="mb-6 rounded-md border border-red-500/40 bg-red-500/10 px-4 py-3 text-sm text-red-200">
            <strong>Preview seed failed.</strong> The app may not show realistic
            data until the developer fixes it.
            {view.seedError && (
              <pre className="mt-2 max-h-32 overflow-auto whitespace-pre-wrap font-mono text-xs text-red-300/80">
                {view.seedError}
              </pre>
            )}
          </div>
        )}

        {/* Preview URLs */}
        <section className="mb-8">
          <h2 className="mb-3 font-semibold">Open the preview</h2>
          {view.services.length === 0 ? (
            <p className="text-sm text-neutral-500">
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
                  className="flex items-center justify-between rounded-md border border-neutral-700 bg-neutral-900 px-4 py-3 text-sm transition hover:border-neutral-500 hover:bg-neutral-800"
                >
                  <span className="font-mono">{svc.url}</span>
                  <ExternalLink className="h-4 w-4 text-neutral-400" />
                </a>
              ))}
            </div>
          )}
        </section>

        {/* Decision */}
        {decided ? (
          <section className="rounded-md border border-neutral-700 bg-neutral-900 px-4 py-5">
            <div className="mb-2 flex items-center gap-2 font-semibold">
              {view.decision === "approved" && <Check className="h-4 w-4 text-green-400" />}
              {view.decision === "changes_requested" && <AlertCircle className="h-4 w-4 text-amber-400" />}
              {view.decision === "denied" && <XIcon className="h-4 w-4 text-red-400" />}
              {view.decision === "approved" && "Approved"}
              {view.decision === "changes_requested" && "Changes requested"}
              {view.decision === "denied" && "Denied"}
            </div>
            {view.decisionComment && (
              <p className="whitespace-pre-wrap text-sm text-neutral-300">
                {view.decisionComment}
              </p>
            )}
            <p className="mt-3 text-xs text-neutral-500">
              by {view.decidedBy || "anonymous"}{" "}
              {view.decidedAt && (
                <>· {new Date(view.decidedAt).toLocaleString()}</>
              )}
            </p>
            <p className="mt-4 text-xs text-neutral-500">
              You can resubmit by reloading and choosing again.
            </p>
          </section>
        ) : (
          <section>
            <h2 className="mb-3 font-semibold">Leave a decision</h2>
            <div className="mb-3 grid grid-cols-1 gap-2 sm:grid-cols-2">
              <input
                type="email"
                placeholder="Your email (optional)"
                value={reviewerEmail}
                onChange={(e) => setReviewerEmail(e.target.value)}
                className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-sm placeholder:text-neutral-600 focus:border-neutral-500 focus:outline-none"
              />
            </div>
            <textarea
              placeholder="Comment (optional but helpful)…"
              value={comment}
              onChange={(e) => setComment(e.target.value)}
              rows={5}
              className="mb-3 w-full rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-sm placeholder:text-neutral-600 focus:border-neutral-500 focus:outline-none"
            />
            <div className="flex flex-col gap-2 sm:flex-row">
              <button
                type="button"
                onClick={() => submit("approved")}
                disabled={submitting}
                className="flex-1 rounded-md bg-green-600 px-4 py-2.5 text-sm font-semibold text-white hover:bg-green-500 disabled:opacity-60"
              >
                Approve
              </button>
              <button
                type="button"
                onClick={() => submit("changes_requested")}
                disabled={submitting}
                className="flex-1 rounded-md bg-amber-600 px-4 py-2.5 text-sm font-semibold text-white hover:bg-amber-500 disabled:opacity-60"
              >
                Request changes
              </button>
              <button
                type="button"
                onClick={() => submit("denied")}
                disabled={submitting}
                className="flex-1 rounded-md border border-neutral-700 bg-neutral-900 px-4 py-2.5 text-sm font-semibold text-neutral-300 hover:bg-neutral-800 disabled:opacity-60"
              >
                Deny
              </button>
            </div>
          </section>
        )}

        <p className="mt-12 text-center text-xs text-neutral-600">
          Powered by kuso · auto-generated reviewer page
        </p>
      </div>
    </div>
  );
}
