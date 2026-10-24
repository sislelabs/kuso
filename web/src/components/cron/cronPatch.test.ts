import { describe, expect, it } from "vitest";
import { onFailureCreateField, policyCreateFields, projectCronPatch, serviceCronPatch, type CronForm } from "./cronPatch";

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

describe("policy + onFailure", () => {
  const policy = { concurrencyPolicy: "Forbid", activeDeadlineSeconds: "600", startingDeadlineSeconds: "300" };

  it("sends only changed policy fields; blank timeout clears to 0", () => {
    const body = serviceCronPatch({
      ...base,
      kind: "service",
      policy: { ...policy, activeDeadlineSeconds: "", concurrencyPolicy: "Replace" },
      initialPolicy: policy,
    });
    expect(body.concurrencyPolicy).toBe("Replace");
    expect(body.activeDeadlineSeconds).toBe(0);
    expect(body).not.toHaveProperty("startingDeadlineSeconds");
  });

  it("never sends onFailure for service crons", () => {
    const body = serviceCronPatch({
      ...base,
      kind: "service",
      onFailure: { webhookURL: "https://hook", secretName: "", secretKey: "" },
      initialOnFailure: { webhookURL: "", secretName: "", secretKey: "" },
    });
    expect(body).not.toHaveProperty("onFailure");
  });

  it("clears a project cron's webhook when the URL is emptied", () => {
    const of = { webhookURL: "https://hook", secretName: "p-shared", secretKey: "HMAC" };
    expect(projectCronPatch({ ...base, onFailure: { ...of, webhookURL: "" }, initialOnFailure: of }).onFailure).toEqual({
      clear: true,
    });
    expect(
      projectCronPatch({ ...base, onFailure: { ...of, webhookURL: "https://new" }, initialOnFailure: of }).onFailure,
    ).toEqual({ webhookURL: "https://new", secretRef: { name: "p-shared", key: "HMAC" } });
  });

  it("rejects a bad deadline and policy", () => {
    expect(() =>
      serviceCronPatch({ ...base, kind: "service", policy: { ...policy, activeDeadlineSeconds: "-1" }, initialPolicy: policy }),
    ).toThrow();
    expect(() => policyCreateFields({ ...policy, concurrencyPolicy: "Sometimes" })).toThrow();
  });

  it("create fields omit blanks", () => {
    expect(policyCreateFields({ concurrencyPolicy: "", activeDeadlineSeconds: "", startingDeadlineSeconds: "" })).toEqual({});
    expect(onFailureCreateField({ webhookURL: "", secretName: "", secretKey: "" })).toEqual({});
  });
});
