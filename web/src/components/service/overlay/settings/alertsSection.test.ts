import { describe, expect, it } from "vitest";
import type { AlertRule } from "@/features/alerts";
import { rulesForService } from "./AlertsSection";

const r = (id: string, project?: string, service?: string): AlertRule => ({
  id, name: id, enabled: true, kind: "http_5xx_rate", project, service,
  windowSeconds: 300, severity: "warn", throttleSeconds: 600, createdAt: "", updatedAt: "",
});

describe("rulesForService", () => {
  it("keeps rules for this service and project-wide ones, drops others", () => {
    const rules = [r("mine", "shop", "web"), r("all", "shop"), r("other", "shop", "api"), r("cluster"), r("elsewhere", "blog", "web")];
    expect(rulesForService(rules, "shop", "web").map((x) => x.id)).toEqual(["mine", "all"]);
  });
});
