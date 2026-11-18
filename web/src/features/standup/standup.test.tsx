import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { StandupResponse } from "../../api/gen/api.gen";
import { stubApi, type Call } from "../../test/fakeApi";
import { renderWidget } from "../../test/renderWidget";
import { dayString, rangeFromDays, stepStandup, type DayRange, type StandupInput, type StandupState } from "./core";
import { StandupPanel } from "./StandupPanel";

const range: DayRange = { from: "2026-01-14T00:00:00.000Z", to: "2026-01-15T00:00:00.000Z" };
const summary = (over: Partial<StandupResponse> = {}): StandupResponse => ({
  projectId: "p1",
  from: range.from,
  to: range.to,
  phase: "ready",
  commits: [{ repoId: "r1", repoName: "shop-api", id: "abc1234", subject: "Fix cart total", author: "Ada", at: "2026-01-14T10:00:00Z" }],
  mrs: [],
  builds: [],
  text: "shop-api\n- abc1234 Fix cart total (Ada)",
  ...over,
});

describe("standup range", () => {
  it("builds a half-open UTC range from local days and counts days inclusively", () => {
    const r = rangeFromDays("2026-01-14", "2026-01-14");
    expect(r).toMatchObject({ ok: true, days: 1 });
    if (r.ok) {
      expect(r.range.from).toBe(new Date(2026, 0, 14).toISOString());
      expect(r.range.to).toBe(new Date(2026, 0, 15).toISOString());
    }
  });
  it("allows 31 days and refuses 32", () => {
    expect(rangeFromDays("2026-01-01", "2026-01-31")).toMatchObject({ ok: true, days: 31 });
    expect(rangeFromDays("2026-01-01", "2026-02-01")).toEqual({ ok: false, message: "Pick at most 31 days." });
    // Across a month with a DST change the day count still ignores clock shifts.
    expect(rangeFromDays("2026-03-01", "2026-03-31")).toMatchObject({ ok: true, days: 31 });
  });
  it("refuses an inverted or malformed range", () => {
    expect(rangeFromDays("2026-01-15", "2026-01-14").ok).toBe(false);
    expect(rangeFromDays("", "2026-01-14").ok).toBe(false);
    expect(rangeFromDays("2026-02-30", "2026-03-01").ok).toBe(false);
  });
  it("dayString is the local calendar day", () => {
    expect(dayString(new Date(2026, 0, 5, 23, 59))).toBe("2026-01-05");
  });
});

describe("standup reducer", () => {
  const empty: StandupState = { tag: "empty" };
  const loading = (attempt: number): StandupState => ({ tag: "loading", attempt, range });

  it("requested -> Loading with one read", () => {
    expect(stepStandup(empty, { tag: "requested", range, attempt: 1 })).toEqual([loading(1), [{ tag: "readSources", attempt: 1, range }]]);
  });
  it("a new request cancels the one in flight, and an older attempt number is ignored", () => {
    const [s, c] = stepStandup(loading(1), { tag: "requested", range, attempt: 2 });
    expect(s).toEqual(loading(2));
    expect(c.map((x) => x.tag)).toEqual(["cancelRead", "readSources"]);
    expect(stepStandup(loading(2), { tag: "requested", range, attempt: 1 })).toEqual([loading(2), []]);
  });
  it("sourcesRead for the current attempt -> Ready / Partial / Failed by the server's phase; an empty summary is Ready", () => {
    expect(stepStandup(loading(1), { tag: "sourcesRead", attempt: 1, response: summary() })[0].tag).toBe("ready");
    expect(stepStandup(loading(1), { tag: "sourcesRead", attempt: 1, response: summary({ phase: "partial" }) })[0].tag).toBe("partial");
    expect(stepStandup(loading(1), { tag: "sourcesRead", attempt: 1, response: summary({ phase: "failed", message: "No source." }) })[0]).toMatchObject({
      tag: "failed",
      reason: "No source.",
    });
    expect(stepStandup(loading(1), { tag: "sourcesRead", attempt: 1, response: summary({ commits: [], text: "" }) })[0].tag).toBe("ready");
  });
  it("a stale answer is ignored", () => {
    const s = loading(2);
    expect(stepStandup(s, { tag: "sourcesRead", attempt: 1, response: summary() })).toEqual([s, []]);
    expect(stepStandup(s, { tag: "readFailed", attempt: 1, reason: "x" })).toEqual([s, []]);
    expect(stepStandup(s, { tag: "cancelled", attempt: 1 })).toEqual([s, []]);
  });
  it("readFailed -> Failed; cancelled -> Empty", () => {
    expect(stepStandup(loading(1), { tag: "readFailed", attempt: 1, reason: "Offline." })[0]).toEqual({ tag: "failed", reason: "Offline.", range });
    expect(stepStandup(loading(1), { tag: "cancelled", attempt: 1 })[0]).toEqual(empty);
  });
  it("polish emits one submission only from Ready/Partial with text; never from refresh", () => {
    const ready: StandupState = { tag: "ready", summary: summary() };
    expect(stepStandup(ready, { tag: "polishRequested", requestId: "k1" })[1]).toEqual([{ tag: "submitPolish", requestId: "k1", range }]);
    expect(stepStandup({ tag: "partial", summary: summary() }, { tag: "polishRequested", requestId: "k1" })[1]).toHaveLength(1);
    for (const s of [empty, loading(1), { tag: "failed", reason: "x", range } as StandupState, { tag: "ready", summary: summary({ text: "  " }) } as StandupState]) {
      expect(stepStandup(s, { tag: "polishRequested", requestId: "k1" })[1]).toEqual([]);
    }
    const inputs: StandupInput[] = [
      { tag: "requested", range, attempt: 5 },
      { tag: "sourcesRead", attempt: 1, response: summary() },
    ];
    for (const input of inputs) {
      const [, cmds] = stepStandup(loading(1), input);
      expect(cmds.some((c) => c.tag === "submitPolish")).toBe(false);
    }
  });
});

const reposBody = {
  counts: { all: 1, uncommitted: 0, behind: 0, ahead: 0, failedPipeline: 0, problems: 0 },
  projects: [{ id: "p1", name: "acme-shop", path: "/w", counts: {}, repos: [] }],
};
const handler =
  (s: () => { status?: number; body: unknown }) =>
  (c: Call) => {
    if (c.path.startsWith("/api/repos?")) return { body: reposBody };
    if (c.path.startsWith("/api/projects/p1/standup?")) return s();
    if (c.path === "/api/projects/p1/standup/polish") return { body: { requestId: (c.body as { requestId: string }).requestId, state: "completed", taskId: "t3", message: "Started an Ask task." } };
    return undefined;
  };

describe("standup widget", () => {
  it("reads once on open, shows source labels, and starts no agent", async () => {
    const api = stubApi(handler(() => ({ body: summary() })));
    await renderWidget(StandupPanel);
    expect((await screen.findByLabelText("Standup summary")).textContent).toContain("Fix cart total");
    expect(screen.getByText(/1 commits \(from git history\)/)).toBeTruthy();
    expect(api.calls.filter((c) => c.path.startsWith("/api/projects/p1/standup?"))).toHaveLength(1);
    expect(api.calls.filter((c) => c.path.endsWith("/polish"))).toHaveLength(0);
    // Refresh reads again and still starts nothing.
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => {
      expect(api.calls.filter((c) => c.path.startsWith("/api/projects/p1/standup?"))).toHaveLength(2);
    });
    expect(api.calls.filter((c) => c.path.endsWith("/polish"))).toHaveLength(0);
  });

  it("Polish is an explicit click: one request with a key, then a link to the visible task", async () => {
    const api = stubApi(handler(() => ({ body: summary() })));
    await renderWidget(StandupPanel);
    await screen.findByLabelText("Standup summary");
    await waitFor(() => {
      expect(screen.getByRole<HTMLButtonElement>("button", { name: "Polish with agent" }).disabled).toBe(false);
    });
    fireEvent.click(screen.getByRole("button", { name: "Polish with agent" }));
    await screen.findByText(/Started an Ask task/);
    const polish = api.calls.filter((c) => c.path.endsWith("/polish"));
    expect(polish).toHaveLength(1);
    expect(polish[0]?.body).toMatchObject({ from: range.from, to: range.to, agent: "mock" });
    expect(typeof (polish[0]?.body as { requestId: string }).requestId).toBe("string");
    expect(screen.getByRole("link", { name: "Open the task" })).toBeTruthy();
  });

  it("copy writes the displayed text from a click and falls back when the clipboard is blocked", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    stubApi(handler(() => ({ body: summary() })));
    await renderWidget(StandupPanel);
    await screen.findByLabelText("Standup summary");
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
    await screen.findByText("Copied.");
    expect(writeText).toHaveBeenCalledWith("shop-api\n- abc1234 Fix cart total (Ada)");
    writeText.mockRejectedValueOnce(new Error("denied"));
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
    await screen.findByText(/Copy was blocked/);
  });

  it("an empty summary is NoStandup, not an error, and cannot be polished", async () => {
    stubApi(handler(() => ({ body: summary({ commits: [], text: "" }) })));
    const { container } = await renderWidget(StandupPanel);
    await waitFor(() => {
      expect(container.querySelector('[data-empty="NoStandup"]')).not.toBeNull();
    });
    expect(screen.getByRole<HTMLButtonElement>("button", { name: "Polish with agent" }).disabled).toBe(true);
  });

  it("partial and truncated are marked with their source labels", async () => {
    stubApi(
      handler(() => ({
        body: summary({ phase: "partial", unavailable: [{ source: "merge_requests", reason: "no token" }], truncated: ["commits"] }),
      })),
    );
    const { container } = await renderWidget(StandupPanel);
    await waitFor(() => {
      expect(container.querySelector('[data-standup="partial"]')).not.toBeNull();
    });
    expect(screen.getByText(/Merge requests \(from the host cache\): no token/)).toBeTruthy();
    expect(container.querySelector('[data-standup="truncated"]')?.textContent).toContain("Commits (from git history)");
  });

  it("a failed read is an error with retry, never NoStandup", async () => {
    stubApi(handler(() => ({ status: 500, body: { code: "internal", message: "Sources failed.", hint: "Try again." } })));
    const { container } = await renderWidget(StandupPanel);
    expect((await screen.findByRole("alert")).textContent).toContain("Sources failed.");
    expect(container.querySelector("[data-empty]")).toBeNull();
    expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
  });

  it("refuses a range over 31 days before any request", async () => {
    const api = stubApi(handler(() => ({ body: summary() })));
    await renderWidget(StandupPanel);
    await screen.findByLabelText("Standup summary");
    fireEvent.change(screen.getByLabelText("First day"), { target: { value: "2026-01-01" } });
    fireEvent.change(screen.getByLabelText("Last day"), { target: { value: "2026-03-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect((await screen.findByRole("alert")).textContent).toBe("Pick at most 31 days.");
    expect(api.calls.filter((c) => c.path.startsWith("/api/projects/p1/standup?"))).toHaveLength(1);
  });

  it("no projects is its own state", async () => {
    stubApi((c) => (c.path.startsWith("/api/repos?") ? { body: { ...reposBody, projects: [] } } : undefined));
    const { container } = await renderWidget(StandupPanel);
    await waitFor(() => {
      expect(container.querySelector('[data-empty="NoProjects"]')).not.toBeNull();
    });
  });
});
