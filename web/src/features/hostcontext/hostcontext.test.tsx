import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { FailedJobsResponse, HostContextStatusDTO, IssuesResponse, MRContextResponse } from "../../api/gen/api.gen";
import { stubApi, type Call, type Reply } from "../../test/fakeApi";
import { renderWidget } from "../../test/renderWidget";
import { describeStatus } from "./status";
import { FailedJobsView, IssuesView, MRContextView } from "./Views";

const ready: HostContextStatusDTO = { phase: "ready" };
const blocked = (reason: string, over: Partial<HostContextStatusDTO> = {}): HostContextStatusDTO => ({
  phase: "blocked",
  reason,
  ...over,
});

describe("host context status", () => {
  it("says what each blocked reason means, with a retry only where a retry can help", () => {
    const noToken = describeStatus(blocked("no_token"));
    expect(noToken).toMatchObject({ tag: "blocked", reason: "no_token", retryable: false });
    expect(describeStatus(blocked("unsupported"))).toMatchObject({ retryable: false });
    expect(describeStatus(blocked("forbidden"))).toMatchObject({ retryable: true });
    expect(describeStatus(blocked("retry_exhausted"))).toMatchObject({ retryable: true });
  });

  it("a rate limit with a deadline says when to try again", () => {
    const v = describeStatus(blocked("retry_exhausted", { expiresAt: "2026-01-01T10:05:00Z" }));
    expect(v.tag === "blocked" && v.text).toMatch(/Try again after/);
    const w = describeStatus({ phase: "waiting", retryAt: "2026-01-01T10:05:00Z" });
    expect(w.tag === "waiting" && w.text).toMatch(/Trying again at/);
  });

  it("a partial status lists the missing parts", () => {
    const v = describeStatus({ phase: "partial", unavailable: [{ source: "checks", reason: "no_token" }] });
    expect(v).toMatchObject({ tag: "partial", gaps: ["checks: no token"] });
  });

  it("an unknown phase is not a success", () => {
    expect(describeStatus({ phase: "mystery" }).tag).toBe("blocked");
  });
});

const jobs = (status: HostContextStatusDTO, over: Partial<FailedJobsResponse> = {}): FailedJobsResponse => ({
  repoId: "r1",
  runId: "run-7",
  jobs: [{ jobId: "j1", name: "unit-tests", stage: "test", log: "FAIL shop_test.go:42", logTruncated: true }],
  status,
  ...over,
});
const issues = (status: HostContextStatusDTO, over: Partial<IssuesResponse> = {}): IssuesResponse => ({
  repoId: "r1",
  issues: [{ number: 12, title: "Checkout total is wrong", labels: ["bug"], updatedAt: "2026-01-01T10:00:00Z" }],
  status,
  ...over,
});

const on = (path: RegExp, reply: () => Reply) => (c: Call) => (path.test(c.path) ? reply() : undefined);

describe("host context views", () => {
  it("failed jobs: shows redacted plain text logs, labels truncation", async () => {
    stubApi(on(/failed-jobs/, () => ({ body: jobs(ready) })));
    await renderWidget(() => <FailedJobsView repoId="r1" repoName="shop-api" runId="run-7" />);
    expect((await screen.findByLabelText("Log of unit-tests")).textContent).toBe("FAIL shop_test.go:42");
    expect(screen.getByText(/This log was cut/)).toBeTruthy();
    expect(screen.getByText(/shop-api, run run-7/)).toBeTruthy();
  });

  it("a log with markup stays plain text", async () => {
    stubApi(on(/failed-jobs/, () => ({ body: jobs(ready, { jobs: [{ jobId: "j1", name: "lint", log: "<img src=x onerror=alert(1)>" }] }) })));
    const { container } = await renderWidget(() => <FailedJobsView repoId="r1" repoName="shop-api" runId="run-7" />);
    await screen.findByLabelText("Log of lint");
    expect(container.querySelector("img")).toBeNull();
  });

  it("blocked without a token is a labelled state with no retry button", async () => {
    stubApi(on(/failed-jobs/, () => ({ body: jobs(blocked("no_token"), { jobs: [] }) })));
    const { container } = await renderWidget(() => <FailedJobsView repoId="r1" repoName="shop-api" runId="run-7" />);
    await waitFor(() => {
      expect(container.querySelector('[data-empty="NoCredentials"]')).not.toBeNull();
    });
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
  });

  it("blocked by the host (rate limit) shows the deadline and a retry", async () => {
    stubApi(on(/failed-jobs/, () => ({ body: jobs(blocked("retry_exhausted", { expiresAt: "2026-01-01T10:05:00Z" }), { jobs: [] }) })));
    await renderWidget(() => <FailedJobsView repoId="r1" repoName="shop-api" runId="run-7" />);
    expect((await screen.findByRole("status")).textContent).toMatch(/Try again after/);
    expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
  });

  it("a read error is an error with retry, not an empty list", async () => {
    stubApi(on(/issues/, () => ({ status: 500, body: { code: "internal", message: "The server hit a problem.", hint: "Try again." } })));
    const { container } = await renderWidget(() => <IssuesView repoId="r1" repoName="shop-api" onImport={() => undefined} />);
    expect((await screen.findByRole("alert")).textContent).toContain("The server hit a problem.");
    expect(container.querySelector('[data-empty="NoIssues"]')).toBeNull();
    expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
  });

  it("a superseded read says to ask again", async () => {
    stubApi(on(/issues/, () => ({ status: 409, body: { code: "superseded", message: "Overtaken." } })));
    await renderWidget(() => <IssuesView repoId="r1" repoName="shop-api" onImport={() => undefined} />);
    expect((await screen.findByRole("alert")).textContent).toContain("A newer change replaced this read");
  });

  it("a malformed body is an error, not an empty list", async () => {
    stubApi(on(/issues/, () => ({ body: { repoId: "r1" } })));
    const { container } = await renderWidget(() => <IssuesView repoId="r1" repoName="shop-api" onImport={() => undefined} />);
    expect((await screen.findByRole("alert")).textContent).toContain("unexpected");
    expect(container.querySelector("[data-empty]")).toBeNull();
  });

  it("issues: an empty ready list is the NoIssues state", async () => {
    stubApi(on(/issues/, () => ({ body: issues(ready, { issues: [] }) })));
    const { container } = await renderWidget(() => <IssuesView repoId="r1" repoName="shop-api" onImport={() => undefined} />);
    await waitFor(() => {
      expect(container.querySelector('[data-empty="NoIssues"]')).not.toBeNull();
    });
  });

  it("issues: partial and truncated are labelled, and Add draft task hands the issue to the shared dialog", async () => {
    stubApi(on(/issues/, () => ({ body: issues({ phase: "partial", unavailable: [{ source: "labels", reason: "forbidden" }] }, { truncated: true }) })));
    const picked: number[] = [];
    const { container } = await renderWidget(() => (
      <IssuesView
        repoId="r1"
        repoName="shop-api"
        onImport={(i) => {
          picked.push(i.number);
        }}
      />
    ));
    await screen.findByText("Checkout total is wrong");
    expect(container.querySelector("[data-partial]")).not.toBeNull();
    expect(container.querySelector("[data-truncated]")).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Add draft task" }));
    expect(picked).toEqual([12]);
  });

  it("an imported issue links its draft and can be started only by its own explicit button", async () => {
    const api = stubApi((c) => {
      if (/issues/.test(c.path)) return { body: issues(ready, { issues: [{ number: 12, title: "Checkout total is wrong", updatedAt: "2026-01-01T10:00:00Z", draftTaskId: "t9" }] }) };
      if (c.path === "/api/tasks/t9/start") return { body: { task: { id: "t9" } } };
      return undefined;
    });
    await renderWidget(() => <IssuesView repoId="r1" repoName="shop-api" onImport={() => undefined} />);
    await screen.findByText("Checkout total is wrong");
    expect(api.calls.filter((c) => c.path.endsWith("/start"))).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Start as Ask task" }));
    await waitFor(() => {
      expect(api.calls.find((c) => c.path === "/api/tasks/t9/start")?.body).toEqual({ mode: "ask" });
    });
  });

  it("MR context: shows scope, pinned head and diff; labels a truncated diff", async () => {
    const body: MRContextResponse = {
      repoId: "r1",
      mr: { number: 5, title: "Add cart badge", url: "https://example.test/mr/5", author: "ada", source: "feat", target: "main", state: "open", updatedAt: "2026-01-01T10:00:00Z" },
      head: "0123456789abcdef",
      diff: "+ added line",
      diffTruncated: true,
      status: ready,
    };
    stubApi(on(/\/mrs\/5\/context/, () => ({ body })));
    const { container } = await renderWidget(() => <MRContextView repoId="r1" repoName="shop-api" number={5} />);
    expect((await screen.findByLabelText("Diff")).textContent).toBe("+ added line");
    expect(screen.getByText(/shop-api, !5/)).toBeTruthy();
    expect(screen.getByText("Pinned to head 01234567")).toBeTruthy();
    expect(container.querySelector("[data-truncated]")).not.toBeNull();
  });
});
