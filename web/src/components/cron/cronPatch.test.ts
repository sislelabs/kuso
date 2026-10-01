import { describe, expect, it } from "vitest";
import { projectCronPatch, serviceCronPatch, type CronForm } from "./cronPatch";

const base: CronForm = {
  kind: "command",
  displayName: " Nightly ",
  schedule: "0 3 * * *",
  suspend: false,
  pinImage: true,
  url: "",
  imageRepo: "ghcr.io/o/r",
  imageTag: "v2",
  cmd: "sh  -c run",
};

describe("projectCronPatch", () => {
  it("keeps pullSecret and pullPolicy the form doesn't edit", () => {
    const body = projectCronPatch(base, {
      repository: "ghcr.io/o/r",
      tag: "v1",
      pullPolicy: "Always",
      pullSecret: "ghcr-creds",
    });
    expect(body.image).toEqual({
      repository: "ghcr.io/o/r",
      tag: "v2",
      pullPolicy: "Always",
      pullSecret: "ghcr-creds",
    });
    expect(body.command).toEqual(["sh", "-c", "run"]);
  });
});

describe("serviceCronPatch", () => {
  it("sends pinImage and the label", () => {
    expect(serviceCronPatch({ ...base, kind: "service" })).toMatchObject({
      pinImage: true,
      displayName: "Nightly",
    });
  });
});

describe("command round-trip", () => {
  const quoted = ["sh", "-c", "echo a  b"];
  const seeded: CronForm = { ...base, cmd: quoted.join(" "), initialCommand: quoted };

  it("omits an untouched command so quoted argv survives", () => {
    expect(projectCronPatch(seeded)).not.toHaveProperty("command");
    expect(serviceCronPatch({ ...seeded, kind: "service" })).not.toHaveProperty("command");
  });

  it("sends an edited command", () => {
    expect(projectCronPatch({ ...seeded, cmd: "echo hi" }).command).toEqual(["echo", "hi"]);
    expect(serviceCronPatch({ ...seeded, kind: "service", cmd: "echo hi" }).command).toEqual([
      "echo",
      "hi",
    ]);
  });
});
