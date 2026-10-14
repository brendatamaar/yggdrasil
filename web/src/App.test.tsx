import { createMemoryHistory } from "@tanstack/react-router";
import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { App } from "./App";
import { createAppRouter } from "./router";

function renderAt(path: string) {
  const router = createAppRouter(createMemoryHistory({ initialEntries: [path] }));
  render(<App router={router} />);
}

describe("app shell", () => {
  it("shows Yggdrasil in the banner and an empty main area", async () => {
    renderAt("/");

    const banner = await screen.findByRole("banner");
    expect(within(banner).getByText("Yggdrasil")).toBeTruthy();
    expect(within(banner).getByRole("button", { name: /change theme/i })).toBeTruthy();
    expect(screen.getByRole("main").textContent).toBe("");
  });

  it("renders the not-found page for unknown paths", async () => {
    renderAt("/some/deep/link");

    expect(await screen.findByRole("heading", { name: "Page not found" })).toBeTruthy();
    expect(screen.getByRole("banner")).toBeTruthy();
  });
});
