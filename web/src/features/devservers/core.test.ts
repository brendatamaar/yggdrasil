import { describe, expect, it } from "vitest";
import { serverId } from "../../api/devservers";
import type { DevServerDTO, DevServerLogMessage } from "../../api/gen/api.gen";
import { followStep, mergeLog, portHref, serverButtons, sortServers, statusLabel, upsertServer } from "./core";

const server = (id: string, status: string): DevServerDTO => ({
  id,
  repoId: "r1",
  repoName: "shop-web",
  command: "npm run dev",
  status,
  ports: [],
});
const line = (seq: number, text = `l${String(seq)}`): DevServerLogMessage => ({ id: "s1", seq, stream: "stdout", line: text });

describe("dev server helpers", () => {
  it("offers only what a state allows", () => {
    expect(serverButtons("stopped")).toEqual({ start: true, stop: false, restart: false });
    expect(serverButtons("failed").start).toBe(true);
    expect(serverButtons("running")).toEqual({ start: false, stop: true, restart: true });
    expect(serverButtons("orphaned")).toEqual({ start: false, stop: true, restart: false });
    expect(serverButtons("starting")).toEqual({ start: false, stop: false, restart: false });
    expect(serverButtons("stopping")).toEqual({ start: false, stop: false, restart: false });
  });

  it("labels states in plain words", () => {
    expect(statusLabel("running")).toBe("Running");
    expect(statusLabel("orphaned")).toBe("Orphaned");
    expect(statusLabel("???")).toBe("Unknown");
  });

  it("links a port to the loopback address", () => {
    expect(portHref(3000)).toBe("http://127.0.0.1:3000");
  });

  it("sorts orphaned and failed first and keeps order inside a group", () => {
    const sorted = sortServers([server("a", "stopped"), server("b", "running"), server("c", "orphaned"), server("d", "failed"), server("e", "running")]);
    expect(sorted.map((s) => serverId(s))).toEqual(["c", "d", "b", "e", "a"]);
  });

  it("upserts by ID", () => {
    const list = [server("a", "stopped")];
    expect(upsertServer(list, server("a", "running"))[0]?.status).toBe("running");
    expect(upsertServer(list, server("b", "running"))).toHaveLength(2);
    expect(upsertServer(undefined, server("b", "running"))).toHaveLength(1);
  });
});

describe("mergeLog", () => {
  it("dedupes by sequence number and keeps order", () => {
    expect(mergeLog([line(1), line(2)], [line(2), line(3)]).map((l) => l.seq)).toEqual([1, 2, 3]);
    expect(mergeLog([line(3)], [line(1)]).map((l) => l.seq)).toEqual([1, 3]);
  });
  it("keeps only the newest lines past the cap", () => {
    const lines = Array.from({ length: 10 }, (_, i) => line(i));
    expect(mergeLog([], lines, 4).map((l) => l.seq)).toEqual([6, 7, 8, 9]);
  });
  it("is the same list when nothing arrives", () => {
    const cur = [line(1)];
    expect(mergeLog(cur, [])).toBe(cur);
  });
});

describe("followStep", () => {
  it("pauses when scrolled up and follows again at the bottom or on resume", () => {
    const f = { tag: "following" } as const;
    const p = followStep(f, { tag: "scrolled", atBottom: false });
    expect(p.tag).toBe("paused");
    expect(followStep(p, { tag: "scrolled", atBottom: false })).toBe(p);
    expect(followStep(p, { tag: "scrolled", atBottom: true }).tag).toBe("following");
    expect(followStep(p, { tag: "resume" }).tag).toBe("following");
  });
});
