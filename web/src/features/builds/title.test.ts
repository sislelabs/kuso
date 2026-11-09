import { describe, expect, it } from "vitest";
import { buildTitle, buildTrigger } from "./title";
import type { DeployBuild } from "./types";

const b = (o: Partial<DeployBuild>): DeployBuild => ({ id: o.id ?? "b", serviceName: "api", status: "succeeded", ...o });
const SHA = "c850e9b0ef35e278cf6374aa4c8e9a609ca32db2";

describe("buildTitle", () => {
  it("uses the commit subject, not the body", () => {
    expect(buildTitle(b({ commitMessage: "Fix login\n\nlong body", triggeredBy: "user" }))).toBe("Fix login");
  });
  it("never says 'Manual redeploy' when the message is missing", () => {
    expect(buildTitle(b({ commitSha: SHA, branch: "main", triggeredBy: "user" }))).toBe("Commit c850e9b on main");
    expect(buildTitle(b({ ref: "main-x7k2p", branch: "main" }))).toBe("Latest main");
  });
});

describe("buildTrigger", () => {
  it("labels pushes, API and first deploys", () => {
    expect(buildTrigger(b({ triggeredBy: "webhook" }), [])?.label).toBe("Push");
    expect(buildTrigger(b({ triggeredBy: "api" }), [])?.label).toBe("API");
    expect(buildTrigger(b({ triggeredBy: "system" }), [])?.label).toBe("First deploy");
  });
  it("tells a retry after a failure from a rebuild of a good commit", () => {
    const failed = b({ id: "1", commitSha: SHA, branch: "main", status: "failed" });
    const retry = b({ id: "2", commitSha: SHA, branch: "main", triggeredBy: "user" });
    expect(buildTrigger(retry, [retry, failed])?.kind).toBe("retry");
    const ok = b({ id: "3", commitSha: SHA, branch: "main" });
    const again = b({ id: "4", commitSha: SHA, branch: "main", triggeredBy: "user" });
    expect(buildTrigger(again, [again, ok])?.kind).toBe("rebuild");
    const fresh = b({ id: "5", commitSha: "deadbeef", branch: "main", triggeredBy: "user" });
    expect(buildTrigger(fresh, [fresh, ok])?.kind).toBe("manual");
  });
});

describe("previousDeployTarget", () => {
  it("skips older images of the live commit when another commit exists", async () => {
    const { previousDeployTarget } = await import("./live");
    const live = b({ id: "3", commitSha: "aaa", branch: "main", imageTag: "t3", liveEnvs: ["production"] });
    const same = b({ id: "2", commitSha: "aaa", branch: "main", imageTag: "t2" });
    const prev = b({ id: "1", commitSha: "bbb", branch: "main", imageTag: "t1" });
    expect(previousDeployTarget([live, same, prev], "production")).toMatchObject({ target: { id: "1" }, sameCommit: false });
    expect(previousDeployTarget([live, same], "production")).toMatchObject({ target: { id: "2" }, sameCommit: true });
    expect(previousDeployTarget([live], "production")).toBeUndefined();
  });
});
