import { describe, expect, it } from "vitest";
import { announce, currentAnnouncement } from "./LiveAnnouncer";

describe("announce", () => {
  it("makes a repeated message a fresh change so it's spoken again", () => {
    announce("api: Live");
    const first = currentAnnouncement();
    announce("api: Live");
    const second = currentAnnouncement();
    expect(first).not.toBe(second);
    expect(second.replace("\u200b", "")).toBe("api: Live");
  });
});
