import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { NoiseCandidateDTO, NoiseResponse, ProposalDTO } from "../../api/gen/api.gen";
import { stubApi, type Call } from "../../test/fakeApi";
import { renderWidget } from "../../test/renderWidget";
import { evidenceText } from "./evidence";
import { NoiseSuggestions } from "./NoiseSuggestions";

const noise = (over: Partial<NoiseResponse> = {}): NoiseResponse => ({
  repoId: "r1",
  available: true,
  candidates: [
    { directory: "node_modules", pattern: "/node_modules/", sampleCount: 80, evidence: { total: 300, sampled: 100, underDirectory: 80 }, incomplete: true },
    { directory: "dist", pattern: "/dist/", sampleCount: 60, evidence: { total: 300, sampled: 100, underDirectory: 60 } },
  ],
  ...over,
});
const proposal: ProposalDTO = {
  id: "p5",
  repoId: "r1",
  repoName: "shop-web",
  kind: "ignore_entries",
  status: "pending",
  source: "user",
  fromAgent: false,
  summary: "",
  lines: ["append to .gitignore: /node_modules/"],
  actionHash: "b".repeat(64),
  attempt: 0,
  createdAt: "2026-01-01T10:00:00Z",
};

const render = () => renderWidget(() => <NoiseSuggestions repoId="r1" />);

describe("noise suggestions", () => {
  it("labels sampled evidence", () => {
    const c = noise().candidates;
    expect(evidenceText(c[0] as NoiseCandidateDTO)).toMatch(/80 of 100 sampled files are under node_modules \(300 untracked in all\)\. Based on a sample\./);
    expect(evidenceText(c[1] as NoiseCandidateDTO)).not.toMatch(/Based on a sample/);
  });

  it("creates one ignore proposal from the picked entries, with a request key, and shows it for confirmation", async () => {
    let created = false;
    const api = stubApi((c: Call) => {
      if (c.path === "/api/repos/r1/noise") return { body: created ? noise({ pendingProposalId: "p5" }) : noise() };
      if (c.path === "/api/repos/r1/noise/proposals") {
        created = true;
        return { body: proposal };
      }
      if (c.path === "/api/proposals/p5") return { body: proposal };
      return undefined;
    });
    await render();
    const create = await screen.findByRole("button", { name: "Create ignore proposal" });
    expect((create as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("checkbox", { name: /\/node_modules\// }));
    fireEvent.click(create);
    await screen.findByRole("button", { name: "Review and confirm" });
    const post = api.calls.filter((c) => c.path === "/api/repos/r1/noise/proposals");
    expect(post).toHaveLength(1);
    expect(post[0]?.body).toMatchObject({ entries: ["/node_modules/"] });
    expect(typeof (post[0]?.body as { requestId: string }).requestId).toBe("string");
    // Nothing was confirmed by creating it.
    expect(api.calls.some((c) => c.path.endsWith("/confirm"))).toBe(false);
  });

  it("shows a pending proposal instead of new suggestions", async () => {
    stubApi((c) => {
      if (c.path === "/api/repos/r1/noise") return { body: noise({ pendingProposalId: "p5" }) };
      if (c.path === "/api/proposals/p5") return { body: proposal };
      return undefined;
    });
    await render();
    await screen.findByText("An ignore proposal is waiting for you.");
    expect(screen.queryByRole("button", { name: "Create ignore proposal" })).toBeNull();
  });

  it("an unreadable repo is labelled, not shown as 'no suggestions'", async () => {
    stubApi((c) => (c.path === "/api/repos/r1/noise" ? { body: noise({ available: false, candidates: [], message: "This repo could not be read." }) } : undefined));
    const { container } = await render();
    await waitFor(() => {
      expect(container.querySelector('[data-noise="unavailable"]')).not.toBeNull();
    });
  });

  it("a failed read is an error", async () => {
    stubApi((c) => (c.path === "/api/repos/r1/noise" ? { status: 500, body: { code: "internal", message: "Hit a problem.", hint: "Try again." } } : undefined));
    await render();
    expect((await screen.findByRole("alert")).textContent).toContain("Hit a problem.");
  });

  it("no candidates: nothing is shown", async () => {
    stubApi((c) => (c.path === "/api/repos/r1/noise" ? { body: noise({ candidates: [] }) } : undefined));
    const { container } = await render();
    await waitFor(() => {
      expect(container.textContent).toBe("");
    });
  });
});
