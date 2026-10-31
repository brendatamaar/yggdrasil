import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { stubApi } from "../../test/fakeApi";
import { needsYouText, stepTrust, type TrustFlow, type TrustInput } from "./core";
import { NeedsYouNotice } from "./NeedsYouNotice";
import { TrustPanel } from "./TrustPanel";

const hash = "c".repeat(64);
const untrusted = {
  repoId: "r1",
  state: "untrusted",
  why: "never_trusted",
  hash,
  files: [{ path: ".claude/settings.json", wouldRun: ["Run `npm run lint` after every edit"] }],
};

const wrap = (ui: React.ReactElement) => (
  <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{ui}</QueryClientProvider>
);

describe("stepTrust", () => {
  const table: [string, TrustFlow, TrustInput, TrustFlow, number][] = [
    ["open", { tag: "idle" }, { tag: "open", hash: "h" }, { tag: "reviewing", hash: "h" }, 0],
    ["confirm sends the shown hash", { tag: "reviewing", hash: "h" }, { tag: "confirm" }, { tag: "sending", hash: "h" }, 1],
    ["confirm from idle", { tag: "idle" }, { tag: "confirm" }, { tag: "idle" }, 0],
    ["config changed under the dialog", { tag: "reviewing", hash: "h" }, { tag: "seen", hash: "g" }, { tag: "changed" }, 0],
    ["same hash", { tag: "reviewing", hash: "h" }, { tag: "seen", hash: "h" }, { tag: "reviewing", hash: "h" }, 0],
    ["error", { tag: "sending", hash: "h" }, { tag: "failed", message: "m" }, { tag: "failed", message: "m" }, 0],
  ];
  it.each(table)("%s", (_n, s, i, want, cmds) => {
    const [next, c] = stepTrust(s, i);
    expect(next).toEqual(want);
    expect(c).toHaveLength(cmds);
  });
});

describe("NeedsYouNotice", () => {
  it.each([
    ["integrity_violated", "Ask mode changed your files"],
    ["cap_exceeded", "This task hit its limit"],
    ["pending_proposals", "Waiting for your review"],
  ])("%s", (reason, title) => {
    render(<NeedsYouNotice reason={reason} />);
    expect(screen.getByText(title)).toBeTruthy();
  });
  it("shows nothing for an unknown or missing reason", () => {
    expect(needsYouText(undefined)).toBeNull();
    expect(needsYouText("something_new")).toBeNull();
  });
  it("integrity failure is an alert", () => {
    render(<NeedsYouNotice reason="integrity_violated" />);
    expect(screen.getByRole("alert")).toBeTruthy();
  });
});

describe("TrustPanel", () => {
  it("lists what would run and trusts with the shown hash", async () => {
    const api = stubApi((c) => {
      if (c.path === "/api/repos/r1/trust" && c.method === "GET") return { body: untrusted };
      if (c.path === "/api/repos/r1/trust" && c.method === "POST") return { body: { ...untrusted, state: "trusted" } };
      return undefined;
    });
    render(wrap(<TrustPanel repoId="r1" repoName="shop-api" />));
    fireEvent.click(await screen.findByRole("button", { name: "Review what would run" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(/npm run lint/)).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "Trust these settings" }));
    await waitFor(() => {
      expect(api.calls.find((c) => c.method === "POST" && c.path === "/api/repos/r1/trust")?.body).toEqual({ hash });
    });
  });

  it("renders nothing for a repo without agent settings", async () => {
    const api = stubApi((c) => (c.path === "/api/repos/r1/trust" ? { body: { repoId: "r1", state: "none", files: [] } } : undefined));
    const { container } = render(wrap(<TrustPanel repoId="r1" />));
    await waitFor(() => {
      expect(api.calls).toHaveLength(1);
    });
    await new Promise((r) => setTimeout(r, 20));
    expect(container.textContent).toBe("");
  });
});
