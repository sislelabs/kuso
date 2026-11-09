import { describe, expect, it } from "vitest";
import { deployStage, failedStep, failedStepFromBuild, failedStepTitle, firstErrorLine, formatStepDuration, parseBuildLog, stallStage } from "./phases";

const SUCCESS = [
  "Cloning into '/workspace/src'...",
  "checked out: c850e9b0ef35e278cf6374aa4c8e9a609ca32db2",
  "==> buildkit: daemon=tcp://kuso-buildkitd:1234",
  "#1 [internal] load build definition from Dockerfile",
  "#1 transferring dockerfile: 287B 0.1s done",
  "#1 DONE 0.1s",
  "#9 [build 2/6] WORKDIR /src",
  "#9 CACHED",
  "#13 [build 6/6] RUN CGO_ENABLED=0 go build -o /out/api .",
  "#13 3.10 downloading",
  "#13 3.10 downloading",
  "#13 3.10 downloading",
  "#13 ",
  "#13 DONE 16.1s",
  "#15 exporting to image",
  "#15 exporting layers",
  "#15 exporting layers 0.3s done",
  "#15 DONE 0.2s",
];

const FAILED_TAIL = [
  "#10 1.045 Reading package lists...",
  "#10 DONE 6.1s",
  "#12 [prod-deps 5/5] RUN npm install -g npm@11   && npm ci --omit=dev",
  "#12 9.005 removed 65 packages",
  "#12 ...",
  "#13 [deps 5/5] RUN npm ci",
  "#13 9.107 removed 65 packages",
  "#13 ...",
  "#12 [prod-deps 5/5] RUN npm install -g npm@11   && npm ci --omit=dev",
  "#12 10.03 npm warn EBADENGINE Unsupported engine {",
  "#12 10.05 npm error code EUSAGE",
  "#12 10.06 npm error",
  "#12 10.06 npm error `npm ci` can only install packages when your package.json and package-lock.json are in sync.",
  '#12 ERROR: process "/bin/sh -c npm ci" did not complete successfully: exit code: 1',
  "#13 [deps 5/5] RUN npm ci",
  "#13 CANCELED",
  "------",
  " > [prod-deps 5/5] RUN npm ci:",
  "------",
  "Dockerfile:33",
  "  33 | >>> RUN npm ci",
  'error: failed to solve: process "/bin/sh -c npm ci" did not complete successfully: exit code: 1',
];

describe("parseBuildLog", () => {
  it("folds a successful BuildKit log into prepare + vertex steps", () => {
    const p = parseBuildLog(SUCCESS);
    expect(p.structured).toBe(true);
    expect(p.truncated).toBe(false);
    expect(p.steps.map((s) => [s.title, s.status])).toEqual([
      ["Clone repository", "done"],
      ["Load build definition from Dockerfile", "done"],
      ["WORKDIR /src", "cached"],
      ["RUN CGO_ENABLED=0 go build -o /out/api .", "done"],
      ["Export and push image", "done"],
    ]);
    const build = p.steps[3];
    expect(build.detail).toBe("build · 6/6");
    expect(build.durationS).toBe(16.1);
    // Three identical lines become one row with a count; the blank one drops.
    expect(build.lines).toHaveLength(1);
    expect(build.lines[0]).toMatchObject({ text: "downloading", repeat: 3, t: 3.1 });
    // The progress line repeated by its "… done" line is dropped.
    expect(p.steps[4].lines.map((l) => l.text)).toEqual(["exporting layers 0.3s done"]);
    expect(p.steps[1].minor).toBe(true);
    expect(p.steps[2].minor).toBe(true);
    expect(p.steps[3].minor).toBe(false);
    expect(p.failedStepId).toBeUndefined();
  });

  it("finds the failing vertex, the cancelled sibling and the error trailer", () => {
    const p = parseBuildLog(FAILED_TAIL, { failed: true });
    expect(p.truncated).toBe(true);
    expect(p.steps[0].title).toBe("Step #10");
    const failing = p.steps.find((s) => s.id === p.failedStepId);
    expect(failing?.title).toBe("RUN npm install -g npm@11 && npm ci --omit=dev");
    expect(failing?.status).toBe("error");
    expect(failing?.errorMessage).toMatch(/did not complete successfully/);
    // Interleaved output lands on the right vertex, the repeated header doesn't.
    expect(failing?.lines.map((l) => l.text)).toHaveLength(5);
    expect(failing?.lines.find((l) => /EBADENGINE/.test(l.text))?.error).toBe(false);
    expect(p.steps.find((s) => s.title === "RUN npm ci")?.status).toBe("canceled");
    const summary = p.steps[p.steps.length - 1];
    expect(summary).toMatchObject({ kind: "summary", title: "Error details", status: "error" });
  });

  it("picks the descriptive error line over npm's code line and buildkit's generic one", () => {
    expect(firstErrorLine(parseBuildLog(FAILED_TAIL, { failed: true }))).toBe(
      "`npm ci` can only install packages when your package.json and package-lock.json are in sync.",
    );
  });

  it("marks the open vertex running while live and reports the deploy stage", () => {
    const live = parseBuildLog(SUCCESS.slice(0, 11), { live: true });
    expect(live.steps[live.steps.length - 1].status).toBe("running");
    expect(deployStage(live)).toBe("build");
    expect(deployStage(parseBuildLog(SUCCESS.slice(0, 2), { live: true }))).toBe("clone");
    expect(deployStage(parseBuildLog(SUCCESS, { live: true }))).toBe("push");
  });

  it("splits archived container sections and buildpacks lifecycle phases", () => {
    const p = parseBuildLog([
      "--- container: clone ---",
      "Cloning into '/workspace/src'...",
      "--- container: build ---",
      "===> DETECTING",
      "heroku/nodejs 3.2.1",
      "===> BUILDING",
      "npm ERR! missing script: build",
    ], { failed: true });
    expect(p.steps.map((s) => [s.title, s.status])).toEqual([
      ["Clone repository", "done"],
      ["Detecting", "done"],
      ["Building", "error"],
    ]);
    expect(p.failedStepId).toBe(p.steps[2].id);
  });

  it("keeps the last frame of carriage-return progress lines on their step", () => {
    const p = parseBuildLog([
      "#10 [base 2/3] RUN apt-get install -y openssl",
      "#10 2.497 (Reading database ... 5%\r(Reading database ... 100%\r(Reading database ... 6096 files installed.)",
      "#10 DONE 6.1s",
    ]);
    expect(p.steps).toHaveLength(1);
    expect(p.steps[0].lines[0].text).toBe("(Reading database ... 6096 files installed.)");
  });

  it("reports unstructured output so the viewer can fall back to raw", () => {
    expect(parseBuildLog(["hello", "world"]).structured).toBe(false);
  });
});

describe("formatStepDuration", () => {
  it("formats short and long spans", () => {
    expect(formatStepDuration(0.1)).toBe("0.1s");
    expect(formatStepDuration(16.1)).toBe("16s");
    expect(formatStepDuration(88.7)).toBe("1m 29s");
    expect(formatStepDuration(undefined)).toBe("");
  });
});

describe("failed step", () => {
  it("names the failing BuildKit step from the log", () => {
    expect(failedStepTitle(parseBuildLog(FAILED_TAIL, { failed: true }))).toBe(
      "RUN npm install -g npm@11 && npm ci --omit=dev · prod-deps · 5/5",
    );
    expect(failedStepTitle(parseBuildLog(SUCCESS))).toBeUndefined();
  });

  it("falls back to the container the server says exited, then the failure kind", () => {
    expect(failedStepFromBuild({ errorMessage: "container exited with code 128 (clone)" })).toBe("Clone repository");
    expect(failedStepFromBuild({ errorMessage: "container exited with code 1 (buildkit)" })).toBe("Run the build");
    expect(failedStepFromBuild({ failureClass: { kind: "lockfile_drift" } })).toBe("Install dependencies");
    expect(failedStepFromBuild({ errorMessage: "boom" })).toBeUndefined();
  });

  it("names the clone step the same from the log preamble and from the server", () => {
    const fromLog = failedStepTitle(parseBuildLog(["Cloning into '/workspace/src'...", "fatal: Remote branch x not found"], { failed: true }));
    expect(fromLog).toBe(failedStepFromBuild({ failureClass: { kind: "clone_ref_missing" } }));
  });

  it("trusts the server's container over the log's last printed step", () => {
    // The clone printed and finished; env-detect never ran, so the log
    // blames the clone.
    const platform = {
      errorMessage: "kuso couldn't start the build: builder image unavailable after 10m0s. ImagePullBackOff: … (env-detect)",
      failureClass: { kind: "build_platform" },
    };
    expect(failedStep(platform, "Clone repository")).toBe("Detect environment variables");
    expect(failedStep({ errorMessage: "container exited with code 1 (nixpacks-plan)" }, "Clone repository")).toBe("Plan nixpacks build");
    // Inside the solve the log's vertex is more specific than "Run the build".
    expect(failedStep({ errorMessage: "container exited with code 1 (buildkit)" }, "RUN npm ci · build · 4/7")).toBe("RUN npm ci · build · 4/7");
    expect(failedStep({ errorMessage: "boom" }, "Clone repository")).toBe("Clone repository");
    // A platform failure with no container never takes the log's guess.
    expect(failedStep({ errorMessage: "kuso couldn't start the build: no node can run it after 20m0s" }, "Clone repository")).toBeUndefined();
  });
});

describe("stallStage", () => {
  it("puts a stuck builder container past the clone", () => {
    expect(stallStage("env-detect")).toBe("build");
    expect(stallStage("buildkit")).toBe("build");
    expect(stallStage("clone")).toBe("clone");
    expect(stallStage("cache-init")).toBe("clone");
    expect(stallStage(undefined)).toBeUndefined();
  });
});
