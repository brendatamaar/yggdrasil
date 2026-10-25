import { fireEvent, render, screen } from "@testing-library/react";
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
  SubagentsFlat,
  SubagentsNested,
  type Capabilities,
  type Event as AgentEvent,
} from "../../api/gen/agents.gen";
import { ResumeButton, StatusLine, Transcript, UsageSummary } from "./Transcript";
import { capText, diffLines, summarizeInput } from "./summary";
import { taskUsage } from "./tree";

let clock = Date.parse("2026-10-16T10:00:00.000Z");
function ev(seq: number, kind: string, extra: Partial<AgentEvent> = {}): AgentEvent {
  clock += 1000;
  return { seq, kind, at: new Date(clock).toISOString(), ...extra };
}

const caps = (over: Partial<Capabilities> = {}): Capabilities => ({
  resume: true,
  costReporting: true,
  planLimits: false,
  permissionHooks: true,
  enforcement: "hook",
  subagents: SubagentsNested,
  subagentLimits: true,
  budgetCap: false,
  ...over,
});

describe("Transcript", () => {
  it("says so when nothing has happened", () => {
    render(<Transcript events={[]} />);
    expect(screen.getByText("Nothing has happened yet.")).toBeTruthy();
  });

  it("renders each event kind", () => {
    render(
      <Transcript
        events={[
          ev(1, EventMessage, { role: RoleUser, text: "Fix the total." }),
          ev(2, EventMessage, { role: RoleAssistant, text: "On it." }),
          ev(3, EventToolCall, { id: "t1", tool: { name: "Read", input: { file_path: "cart.js" } } }),
          ev(4, EventToolResult, { resultFor: "t1", text: "file contents" }),
          ev(5, EventToolResult, { resultFor: "t2", isError: true, text: "denied" }),
          ev(6, EventFileEdit, { file: { path: "src/cart.js", op: "modify", diff: "@@ -1 +1 @@\n-a\n+b\n" } }),
          ev(7, EventCommand, { command: { line: "npm test", exitCode: 1, output: "3 failed" } }),
          ev(8, EventError, { error: "The agent wasn't allowed to use Bash here." }),
          ev(9, EventStatus, { status: RunCompleted }),
        ]}
      />,
    );
    expect(screen.getByText("Prompt")).toBeTruthy();
    expect(screen.getByText("Fix the total.")).toBeTruthy();
    expect(screen.getByText("On it.")).toBeTruthy();
    expect(screen.getByText("Tool: Read")).toBeTruthy();
    expect(screen.getByText(/^cart\.js/)).toBeTruthy(); // short input summary
    expect(screen.getByText("Tool result (error)")).toBeTruthy(); // result without a call stands alone
    expect(screen.getByText("src/cart.js")).toBeTruthy();
    expect(screen.getByText("+b").className).toContain("text-ok"); // diff line
    expect(screen.getByText("-a").className).toContain("text-danger");
    expect(screen.getByText("exit code 1")).toBeTruthy();
    expect(screen.getByText("3 failed")).toBeTruthy(); // failed command opens by default
    expect(screen.getByRole("alert").textContent).toContain("wasn't allowed to use Bash");
    expect(screen.getByText("The agent finished.")).toBeTruthy();
  });

  it("shows a tool result inside its call, not twice", () => {
    render(
      <Transcript
        events={[
          ev(1, EventToolCall, { id: "t1", tool: { name: "Read", input: { file_path: "a.js" } } }),
          ev(2, EventToolResult, { resultFor: "t1", text: "RESULT-BODY" }),
        ]}
      />,
    );
    expect(screen.queryByText("RESULT-BODY")).toBeNull(); // collapsed
    fireEvent.click(screen.getByRole("button", { name: /Tool: Read/ }));
    expect(screen.getAllByText("RESULT-BODY")).toHaveLength(1);
    expect(screen.getByText(/"file_path": "a.js"/)).toBeTruthy(); // full input
    expect(screen.queryByText("Tool result")).toBeNull();
  });

  it("toggles with the keyboard-reachable button and announces its state", () => {
    render(<Transcript events={[ev(1, EventToolCall, { id: "t1", tool: { name: "Grep", input: { pattern: "total" } } })]} />);
    const btn = screen.getByRole("button", { name: /Tool: Grep/ });
    expect(btn.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(btn); // Enter and Space on a button fire click
    expect(btn.getAttribute("aria-expanded")).toBe("true");
    expect(document.getElementById(btn.getAttribute("aria-controls") ?? "")?.textContent).toContain("total");
    fireEvent.click(btn);
    expect(btn.getAttribute("aria-expanded")).toBe("false");
  });

  it("renders agent text as plain text, never as markup", () => {
    const evil = '<script>window.__pwned = 1</script><img src=x onerror="window.__pwned=1">';
    const { container } = render(
      <Transcript
        events={[
          ev(1, EventMessage, { role: RoleAssistant, text: evil }),
          ev(2, EventToolCall, { id: "t", tool: { name: "<b>Bold</b>", input: { command: evil } } }),
          ev(3, EventCommand, { command: { line: evil, exitCode: 2, output: evil } }),
          ev(4, EventFileEdit, { file: { path: "<i>p</i>", op: "modify", diff: `+${evil}` } }),
          ev(5, EventError, { error: evil }),
        ]}
      />,
    );
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("b")).toBeNull();
    expect(container.querySelector("i")).toBeNull();
    expect(container.textContent).toContain("<script>window.__pwned = 1</script>");
  });

  it("nests sub-agents, collapses them and shows their stats", () => {
    render(
      <Transcript
        events={[
          ev(1, EventSubagentStart, { id: "sa", text: "Read the router" }),
          ev(2, EventToolCall, { parentId: "sa", tool: { name: "Read" } }),
          ev(3, EventUsage, { parentId: "sa", usage: { inputTokens: 800, outputTokens: 200 } }),
          ev(4, EventSubagentEnd, { parentId: "sa" }),
          ev(5, EventToolCall, { parentId: "ghost", tool: { name: "Lost" } }),
        ]}
      />,
    );
    const toggle = screen.getByRole("button", { name: "Sub-agent: Read the router" });
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByText(/1 tool call · 3.0 s · 1.0k tokens/)).toBeTruthy();
    expect(screen.getByText("Tool: Read")).toBeTruthy();
    fireEvent.click(toggle);
    expect(screen.queryByText("Tool: Read")).toBeNull();
    expect(screen.getByText("Tool: Lost")).toBeTruthy(); // orphan still shown at the root
  });

  it("shows sub-agent activity flat when the agent can't link it", () => {
    const events = [
      ev(1, EventSubagentStart, { id: "sa", text: "Read the router" }),
      ev(2, EventToolCall, { parentId: "sa", tool: { name: "Read" } }),
    ];
    render(<Transcript events={events} capabilities={caps({ subagents: SubagentsFlat })} />);
    expect(screen.getByText(/doesn't link sub-agent activity/)).toBeTruthy();
    expect(screen.getByText("Tool: Read")).toBeTruthy();
  });

  it("caps long output until asked, and long transcripts until asked", () => {
    const long = Array.from({ length: 300 }, (_, i) => `line ${String(i)}`).join("\n");
    const many = Array.from({ length: 250 }, (_, i) => ev(i + 1, EventStatus, { status: `s${String(i)}` }));
    const { unmount } = render(<Transcript events={[ev(1, EventCommand, { command: { line: "x", exitCode: 1, output: long } })]} />);
    expect(screen.queryByText(/line 299/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Show more/ }));
    expect(screen.getByText(/line 299/)).toBeTruthy();
    unmount();

    render(<Transcript events={many} />);
    expect(screen.queryByText("s0")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Show earlier events (50)" }));
    expect(screen.getByText("s0")).toBeTruthy();
  });
});

describe("capabilities", () => {
  const usage = taskUsage([ev(1, EventUsage, { usage: { inputTokens: 1000, outputTokens: 0, costUsd: 0.5, turns: 1 } })]);

  it("shows cost when the agent reports it", () => {
    render(<UsageSummary usage={usage} capabilities={caps()} />);
    expect(screen.getByText(/1\.0k tokens · \$0\.5000 · 1 turn/)).toBeTruthy();
  });

  it("greys cost out with a reason when the agent can't report it", () => {
    render(<UsageSummary usage={usage} capabilities={caps({ costReporting: false })} />);
    expect(screen.queryByText(/\$0\.5000/)).toBeNull();
    const note = screen.getByText("Cost not reported");
    expect(note.getAttribute("aria-disabled")).toBe("true");
    expect(note.getAttribute("aria-label")).toContain("does not report what a run costs");
  });

  it("greys Resume out when the agent can't resume, and keeps it live otherwise", () => {
    const { unmount } = render(<ResumeButton capabilities={caps({ resume: false })} onResume={() => undefined} />);
    expect(screen.getByRole("button", { name: "Resume" }).getAttribute("aria-disabled")).toBe("true");
    unmount();
    let called = 0;
    render(<ResumeButton capabilities={caps()} onResume={() => {
          called++;
        }}
      />);
    fireEvent.click(screen.getByRole("button", { name: "Resume" }));
    expect(called).toBe(1);
  });
});

describe("StatusLine", () => {
  it("counts tool work and elapsed time", () => {
    render(<StatusLine label="Running" events={[ev(1, EventMessage), ev(2, EventToolCall), ev(3, EventCommand)]} />);
    expect(screen.getByRole("status").textContent).toBe("Running · 2 tool calls · 2.0 s");
  });
});

describe("summary helpers", () => {
  it("summarizes inputs on one short line", () => {
    expect(summarizeInput({ command: "ls\n  -la", other: 1 })).toBe("ls -la");
    expect(summarizeInput({ a: 1 })).toBe("");
    expect(summarizeInput("x".repeat(200)).length).toBe(80);
  });
  it("caps text and classifies diff lines", () => {
    expect(capText("a\nb\nc", 2).shown).toBe("a\nb");
    expect(diffLines("--- a\n+++ b\n@@ -1 +1 @@\n-x\n+y\n z\n").map((l) => l.kind)).toEqual(["meta", "meta", "hunk", "del", "add", "ctx"]);
  });
});
