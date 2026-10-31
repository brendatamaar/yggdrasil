import { describe, expect, it } from "vitest";
import { pickAgent, toChoices, writeModesOffFromHealth } from "./agents";
import { switchTargets, visibleModes } from "./core";

const caps = { resume: false, costReporting: false, subagents: "none", enforcement: "none" };
const choices = toChoices([
  { id: "a", label: "A", available: false, reason: "not installed", capabilities: caps },
  { id: "b", label: "B", available: true, capabilities: caps },
  { id: "c", label: "C", available: true, capabilities: caps },
]);

describe("composer reducers", () => {
  it.each([
    ["wanted and available", "c", "c"],
    ["wanted but unavailable", "a", "b"],
    ["unknown", "zzz", "b"],
  ])("pickAgent: %s", (_n, wanted, want) => {
    expect(pickAgent(choices, wanted)).toBe(want);
  });
  it("pickAgent with nothing available is empty", () => {
    expect(pickAgent([], "mock")).toBe("");
    expect(pickAgent(choices.slice(0, 1), "a")).toBe("");
  });
  it("keeps the reason of an unavailable agent", () => {
    expect(choices[0]?.reason).toBe("not installed");
  });
  it.each([
    [undefined, false],
    [{ writeModes: true }, false],
    [{ writeModes: false }, true],
  ])("writeModes from health %j", (health, off) => {
    expect(writeModesOffFromHealth(health)).toBe(off);
  });
  it("write modes off leaves Ask only", () => {
    expect(visibleModes(false)).toEqual(["ask"]);
    expect(switchTargets("ask", "code")).toEqual(["code"]);
  });
});
