import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { BoardCardDTO } from "../api/gen/api.gen";
import { stubApi } from "../test/fakeApi";
import { maxAgentsError, roomHintText, usageLine, usageTotals } from "./settingsModel";
import { SettingsPage } from "./settingsPage";

const card = (known: boolean, tokens: number, micro: number) =>
  ({
    usage: {
      inputTokens: tokens,
      outputTokens: 0,
      cacheWriteTokens: 0,
      cacheReadTokens: 0,
      costMicroUsd: micro,
      costKnown: known,
      durationMs: 0,
    },
  }) as BoardCardDTO;

const wrap = (ui: React.ReactElement) => (
  <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{ui}</QueryClientProvider>
);

describe("settings model", () => {
  it("usage line", () => {
    expect(usageLine(usageTotals([]))).toBe("No tasks yet.");
    expect(usageLine(usageTotals([card(true, 1500, 2_000_000)]))).toContain("$2.00");
    expect(usageLine(usageTotals([card(false, 10, 0)]))).toContain("cost not reported");
    expect(usageLine(usageTotals([card(true, 10, 1_000_000), card(false, 10, 0)]))).toContain("at least");
  });
  it("bounds", () => {
    const b = { min: 1, max: 16 };
    expect(maxAgentsError("4", b)).toBeNull();
    for (const bad of ["", "0", "17", "2.5", "x"]) expect(maxAgentsError(bad, b)).not.toBeNull();
  });
  it("room hint", () => {
    expect(roomHintText({ raiseTo: 0 }, 2)).toBeNull();
    expect(roomHintText({ raiseTo: 4 }, 2)).toContain("4");
  });
});

describe("SettingsPage", () => {
  it("saves maxAgents and shows the usage line", async () => {
    const api = stubApi((c) => {
      if (c.path === "/api/board/max-agents" && c.method === "GET")
        return { body: { maxAgents: 2, min: 1, max: 16, hint: { raiseTo: 4 } } };
      if (c.path === "/api/board/max-agents" && c.method === "PUT")
        return { body: { maxAgents: 4, min: 1, max: 16, hint: { raiseTo: 0 } } };
      if (c.path === "/api/board")
        return { body: { maxAgents: 2, active: 2, hint: { raiseTo: 4 }, cards: [card(true, 2000, 500_000)] } };
      return undefined;
    });
    render(wrap(<SettingsPage />));
    fireEvent.click(await screen.findByRole("button", { name: "Use 4" }));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => {
      expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({ maxAgents: 4 });
    });
    expect((await screen.findByTestId("usage-line")).textContent).toContain("1 task");
  });

  it("refuses an out-of-range value", async () => {
    stubApi((c) =>
      c.path === "/api/board/max-agents" ? { body: { maxAgents: 2, min: 1, max: 16, hint: { raiseTo: 0 } } } : undefined,
    );
    render(wrap(<SettingsPage />));
    const input = await screen.findByLabelText<HTMLInputElement>("Run up to");
    await waitFor(() => {
      expect(input.value).toBe("2");
    });
    fireEvent.change(input, { target: { value: "40" } });
    expect((screen.getByRole<HTMLButtonElement>("button", { name: "Save" })).disabled).toBe(true);
    expect(screen.getByText(/whole number from 1 to 16/)).toBeTruthy();
  });
});
