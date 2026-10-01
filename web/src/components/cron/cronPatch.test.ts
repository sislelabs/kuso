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
