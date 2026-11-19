import { describe, expect, it } from "vitest";
import { normalizeReviewerView, shouldPollReview } from "./reviewerView";

const base = {
  project: "p",
  prNumber: 1,
  prTitle: "t",
  prBody: "",
  prAuthor: "a",
  baseRef: "main",
  headRef: "feat",
  seedPhase: "succeeded",
  decision: "",
  closed: false,
};

describe("normalizeReviewerView", () => {
  it("turns null services into an empty list", () => {
    expect(normalizeReviewerView({ ...base, services: null }).services).toEqual([]);
  });
  it("keeps services as sent", () => {
    const services = [{ service: "web", url: "https://x" }];
    expect(normalizeReviewerView({ ...base, services }).services).toBe(services);
  });
});

describe("shouldPollReview", () => {
  const view = (over: Partial<typeof base>) => normalizeReviewerView({ ...base, services: [], ...over });
  it("polls while the seed runs", () => {
    expect(shouldPollReview(view({ seedPhase: "running" }))).toBe(true);
    expect(shouldPollReview(null)).toBe(true);
  });
  it("stops once the seed is done, absent, or the review is decided or closed", () => {
    expect(shouldPollReview(view({ seedPhase: "succeeded" }))).toBe(false);
    expect(shouldPollReview(view({ seedPhase: "failed" }))).toBe(false);
    expect(shouldPollReview(view({ seedPhase: "" }))).toBe(false);
    expect(shouldPollReview(view({ seedPhase: "running", decision: "approved" }))).toBe(false);
    expect(shouldPollReview(view({ seedPhase: "running", closed: true }))).toBe(false);
  });
});
