import { createMemoryHistory } from "@tanstack/react-router";
import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../../App";
import { WSClient } from "../../api/ws";
import { createAppRouter } from "../../router";
import { useHeroStore } from "../../stores/hero";
import { stubApi } from "../../test/fakeApi";
import { FakeSocket } from "../../test/fakeSocket";
import { SUGGESTIONS } from "./suggestions";

function renderHome() {
  stubApi();
  const router = createAppRouter(createMemoryHistory({ initialEntries: ["/"] }));
  render(<App router={router} socket={new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket })} />);
}

function mockMotion(reduce: boolean) {
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: q.includes("reduced-motion") ? reduce : true,
    media: q,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
  }));
}

describe("home hero", () => {
  beforeEach(() => {
    FakeSocket.instances = [];
    useHeroStore.setState({ mode: "auto" });
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fills the composer from a chip without submitting", async () => {
    renderHome();
    const chip = await screen.findByRole("button", { name: SUGGESTIONS[0] ?? "" });
    fireEvent.click(chip);
    const prompt = screen.getByLabelText<HTMLTextAreaElement>("What should the agent look into?");
    expect(prompt.value).toBe(SUGGESTIONS[0]);
    expect(document.activeElement).toBe(prompt);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("paints the scene for a fixed mode and removes it when off", async () => {
    useHeroStore.setState({ mode: "dusk" });
    renderHome();
    expect((await screen.findByTestId("hero-scene")).getAttribute("data-phase")).toBe("dusk");

    fireEvent.change(screen.getByLabelText("Scene"), { target: { value: "off" } });
    expect(screen.queryByTestId("hero-scene")).toBeNull();
    expect(useHeroStore.getState().mode).toBe("off");
  });

  it("animates unless the user prefers reduced motion", async () => {
    mockMotion(false);
    renderHome();
    expect((await screen.findByTestId("hero-scene")).getAttribute("data-animated")).toBe("true");
  });

  it("does not animate with prefers-reduced-motion", async () => {
    mockMotion(true);
    renderHome();
    const scene = await screen.findByTestId("hero-scene");
    expect(scene.getAttribute("data-animated")).toBe("false");
    expect(scene.getAttribute("class")).not.toContain("hero-animated");
  });
});
