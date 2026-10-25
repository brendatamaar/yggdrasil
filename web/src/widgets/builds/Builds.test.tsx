import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { BuildDTO } from "../../api/gen/api.gen";
import { renderWidget } from "../../test/renderWidget";
import { stubApi } from "../../test/fakeApi";
import { Builds } from "./Builds";

const build = (id: string, status: string, name: string): BuildDTO => ({
  id,
  status,
  target: "repos",
  actor: "user",
  queuedAt: "2026-01-01T10:00:00Z",
  steps: [{ index: 0, repoId: "r1", name, status, argv: ["go", "build"], attempt: 1, logTruncated: false }],
  artifactsCollected: false,
  artifacts: [],
});
const err = { status: 500, body: { code: "internal", message: "Something went wrong.", hint: "Try again." } };

describe("Builds widget", () => {
  it("shows a loading state first", async () => {
    stubApi(() => ({ body: { builds: [] } }));
    await renderWidget(Builds);
    expect(screen.getByText("Loading…")).toBeTruthy();
  });

  it("puts running and queued builds before finished ones and links to the build page", async () => {
    stubApi(() => ({
      body: { builds: [build("b1", "succeeded", "Build shop-api"), build("b2", "running", "Build shop-web")] },
    }));
    await renderWidget(Builds);
    const links = await screen.findAllByRole("link");
    expect(links.map((l) => l.textContent)).toEqual(["Build shop-web", "Build shop-api"]);
    expect(links[0]?.getAttribute("href")).toBe("/builds/b2");
    expect(screen.getByText("Running")).toBeTruthy();
    expect(screen.getByText("Succeeded")).toBeTruthy();
  });

  it("explains an empty list", async () => {
    stubApi(() => ({ body: { builds: [] } }));
    await renderWidget(Builds);
    expect(await screen.findByText("No builds yet.")).toBeTruthy();
  });

  it("shows an error", async () => {
    stubApi(() => err);
    await renderWidget(Builds);
    expect((await screen.findByRole("alert")).textContent).toContain("Something went wrong.");
  });
});
