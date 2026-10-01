export interface ReviewerView {
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

type WireReviewerView = Omit<ReviewerView, "services"> & {
  services: ReviewerView["services"] | null;
};

// The server can marshal a nil Go slice as `null` when no service opted
// into a reviewer URL.
export function normalizeReviewerView(data: WireReviewerView): ReviewerView {
  return { ...data, services: data.services ?? [] };
}
