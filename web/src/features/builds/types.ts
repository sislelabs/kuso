import type { BuildSummary } from "@/features/services/api";

// DeployBuild is BuildSummary plus the promotion fields newer servers
// send. Every addition is optional so the UI still works against an
// older server that omits them.
export interface DeployBuild extends BuildSummary {
  // Env-group names where this build is the one currently live.
  liveEnvs?: string[];
  waitingFor?: string;
  notPromotedReason?: string;
  cancelReason?: string;
  releaseJob?: string;
  releaseLogTail?: string;
}

export type BuildRowStatus =
  | "active"
  | "superseded"
  | "failed"
  | "release-failed"
  | "running"
  | "pending"
  | "queued"
  | "cancelled"
  | "unknown";
