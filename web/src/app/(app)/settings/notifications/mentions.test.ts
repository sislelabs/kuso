import { describe, expect, it } from "vitest";
import { cleanMentionRules } from "./mentions";

const catalogue = [
  { type: "build.succeeded", defaultMention: "" },
  { type: "backup.failed", defaultMention: "@here" },
];

describe("cleanMentionRules", () => {
  it("drops a none rule that matches the event default", () => {
    expect(cleanMentionRules({ "build.succeeded": "none" }, catalogue)).toEqual({});
  });

  it("keeps a none opt-out on an event that defaults to @here", () => {
    expect(cleanMentionRules({ "backup.failed": "none" }, catalogue)).toEqual({
      "backup.failed": "none",
    });
  });

  it("keeps a none rule that overrides a * rule", () => {
    const m = { "*": "@here", "build.succeeded": "none" };
    expect(cleanMentionRules(m, catalogue)).toEqual(m);
  });

  it("drops a rule equal to the * rule", () => {
    expect(cleanMentionRules({ "*": "@here", "backup.failed": "@here" }, catalogue)).toEqual({
      "*": "@here",
    });
  });

  it("keeps everything without a catalogue", () => {
    expect(cleanMentionRules({ "build.succeeded": "none" }, undefined)).toEqual({
      "build.succeeded": "none",
    });
  });
});
