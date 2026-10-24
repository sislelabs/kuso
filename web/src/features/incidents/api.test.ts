import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api-client", () => ({ api: apiMock }));

import { isOpenIncident, listIncidents, resolveIncident, sendIncidentFeedback } from "./api";

describe("incidents api", () => {
  beforeEach(() => apiMock.mockReset());

  it("unwraps the envelope and pages by offset", async () => {
    apiMock.mockResolvedValue({ incidents: [{ id: "a" }] });
    await expect(listIncidents({ state: "pr_open", offset: 50 })).resolves.toEqual([{ id: "a" }]);
    expect(apiMock).toHaveBeenCalledWith("/api/incidents?limit=50&offset=50&state=pr_open");
  });

  it("treats a null list as empty", async () => {
    apiMock.mockResolvedValue({ incidents: null });
    await expect(listIncidents({})).resolves.toEqual([]);
    expect(apiMock).toHaveBeenCalledWith("/api/incidents?limit=50");
  });

  it("posts decisions and resolve to encoded paths", async () => {
    apiMock.mockResolvedValue({ ok: true });
    await sendIncidentFeedback("x/y", { decision: "go" });
    expect(apiMock).toHaveBeenCalledWith("/api/incidents/x%2Fy/feedback", {
      method: "POST",
      body: { decision: "go" },
    });
    await resolveIncident("x/y");
    expect(apiMock).toHaveBeenCalledWith("/api/incidents/x%2Fy/resolve", { method: "POST" });
  });

  it("classifies open states", () => {
    expect(isOpenIncident("awaiting_feedback")).toBe(true);
    expect(isOpenIncident("timed_out")).toBe(false);
  });
});
