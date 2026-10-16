import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  EventCommand,
  EventError,
  EventFileEdit,
  EventMessage,
  EventStatus,
  EventSubagentEnd,
  EventSubagentStart,
  EventToolCall,
  EventToolResult,
  EventUsage,
  RoleAssistant,
  RoleUser,
  RunCompleted,
  type Event as AgentEvent,
} from "../../api/gen/agents.gen";
import { Transcript, UsageSummary } from "./Transcript";

let clock = Date.parse("2026-10-16T10:00:00.000Z");
function ev(seq: number, kind: string, extra: Partial<AgentEvent> = {}): AgentEvent {
  clock += 1000;
  return { seq, kind, at: new Date(clock).toISOString(), ...extra };
}

describe("Transcript", () => {
  it("says so when nothing has happened", () => {
    render(<Transcript events={[]} />);
    expect(screen.getByText("Nothing has happened yet.")).toBeTruthy();
  });

  it("renders messages, tool calls, results, edits, commands and errors", () => {
    render(
      <Transcript
        events={[
          ev(1, EventMessage, { role: RoleUser, text: "Fix the total." }),
          ev(2, EventMessage, { role: RoleAssistant, text: "On it." }),
          ev(3, EventToolCall, { id: "t1", tool: { name: "Read", input: { file_path: "cart.js" } } }),
          ev(4, EventToolResult, { resultFor: "t1", text: "file contents" }),
          ev(5, EventToolResult, { resultFor: "t2", isError: true, text: "denied" }),
          ev(6, EventFileEdit, { file: { path: "src/cart.js", op: "modify", diff: "@@ edit @@\n-a\n+b\n" } }),
          ev(7, EventCommand, { command: { line: "npm test" } }),
          ev(8, EventError, { error: "The agent wasn't allowed to use Bash here." }),
          ev(9, EventStatus, { status: RunCompleted }),
        ]}
      />,
    );
    expect(screen.getByText("Prompt")).toBeTruthy();
    expect(screen.getByText("Fix the total.")).toBeTruthy();
    expect(screen.getByText("On it.")).toBeTruthy();
    expect(screen.getByText("Tool: Read")).toBeTruthy();
    expect(screen.getByText(/"file_path": "cart.js"/)).toBeTruthy(); // input as JSON text
    expect(screen.getByText("Tool result")).toBeTruthy();
    expect(screen.getByText("Tool result (error)")).toBeTruthy();
    expect(screen.getByText("src/cart.js")).toBeTruthy();
    expect(screen.getByText(/-a/)).toBeTruthy();
    expect(screen.getByText("$ npm test")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain("wasn't allowed to use Bash");
    expect(screen.getByText("The agent finished.")).toBeTruthy();
  });

  it("nests what a sub-agent did and shows its tool calls, duration and tokens", () => {
    render(
      <Transcript
        events={[
          ev(1, EventSubagentStart, { id: "sa1", text: "Review the cart" }),
          ev(2, EventToolCall, { parentId: "sa1", tool: { name: "Read" } }),
          ev(3, EventSubagentStart, { id: "sa2", parentId: "sa1", text: "Check the README" }),
          ev(4, EventMessage, { parentId: "sa2", role: RoleAssistant, text: "Inner message" }),
          ev(5, EventUsage, { parentId: "sa2", usage: { inputTokens: 1500, outputTokens: 500 } }),
          ev(6, EventSubagentEnd, { parentId: "sa2" }),
          ev(7, EventSubagentEnd, { parentId: "sa1" }),
        ]}
      />,
    );
    const outer = screen.getByText("Sub-agent: Review the cart").closest("details");
    expect(outer).not.toBeNull();
    if (!outer) return;
    const inner = within(outer).getByText("Sub-agent: Check the README").closest("details");
    expect(inner).not.toBeNull();
    if (!inner) return;
    expect(within(inner).getByText("Inner message")).toBeTruthy();
    // The outer sub-agent holds the inner one, so the inner message is not a sibling.
    expect(within(outer).getByText("Tool: Read")).toBeTruthy();
    expect(outer.textContent).toContain("1 tool call");
    expect(outer.textContent).toContain("2.0k tokens");
    expect(outer.textContent).toMatch(/\d+\.\d s/);
  });

  it("flags an event whose parent is missing", () => {
    render(<Transcript events={[ev(1, EventSubagentStart, { id: "x", parentId: "ghost", text: "Lost" })]} />);
    expect(screen.getByText("Sub-agent: Lost (detached)")).toBeTruthy();
  });

  it("does not show usage events or end markers as lines of their own", () => {
    const { container } = render(
      <Transcript
        events={[
          ev(1, EventMessage, { role: RoleAssistant, text: "Only this." }),
          ev(2, EventUsage, { usage: { inputTokens: 5 } }),
          ev(3, EventSubagentEnd, { parentId: "nowhere" }),
        ]}
      />,
    );
    expect(container.querySelectorAll("li")).toHaveLength(1);
  });

  it("renders everything the server sent as text, never as markup", () => {
    const evil = `<img src=x onerror="alert(1)"><script>alert(2)</script>`;
    const { container } = render(
      <Transcript
        events={[
          ev(1, EventMessage, { role: RoleAssistant, text: evil }),
          ev(2, EventToolCall, { tool: { name: "Read", input: { html: evil } } }),
          ev(3, EventToolResult, { text: evil }),
          ev(4, EventFileEdit, { file: { path: evil, op: "modify", diff: evil } }),
          ev(5, EventCommand, { command: { line: evil } }),
          ev(6, EventError, { error: evil }),
          ev(7, EventSubagentStart, { id: "s", text: evil }),
        ]}
      />,
    );
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("script")).toBeNull();
    expect(container.textContent).toContain("<img src=x");
  });
});

describe("UsageSummary", () => {
  it("shows tokens, cost and turns", () => {
    const { container } = render(<UsageSummary inputTokens={1200} outputTokens={300} costUsd={0.0044} turns={2} />);
    expect(container.textContent).toBe("1.5k tokens · $0.0044 · 2 turns");
  });

  it("shows nothing before the first usage", () => {
    const { container } = render(<UsageSummary inputTokens={0} outputTokens={0} costUsd={0} turns={0} />);
    expect(container.textContent).toBe("");
  });
});
