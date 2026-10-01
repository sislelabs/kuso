import { describe, expect, it } from "vitest";
import { normalizeReviewerView } from "./reviewerView";

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
