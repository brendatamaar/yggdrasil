import { describe, expect, it } from "vitest";
import type { ProposalDTO } from "../../api/gen/api.gen";
import { describeProposal, groupByChangeSet, stepConfirm, type ConfirmInput, type ConfirmState } from "./core";

const idle: ConfirmState = { tag: "idle" };
const reviewing: ConfirmState = { tag: "reviewing", id: "p1", hash: "h1" };
const sending: ConfirmState = { tag: "sending", id: "p1", hash: "h1" };

describe("stepConfirm", () => {
  const table: [string, ConfirmState, ConfirmInput, ConfirmState, string[]][] = [
    ["open from idle", idle, { tag: "open", id: "p1", hash: "h1" }, reviewing, []],
    ["open while sending is ignored", sending, { tag: "open", id: "p2", hash: "h2" }, sending, []],
    ["confirm sends the hash that was shown", reviewing, { tag: "confirm" }, sending, ["send p1 h1"]],
    ["confirm from idle does nothing", idle, { tag: "confirm" }, idle, []],
    ["confirm twice sends once", sending, { tag: "confirm" }, sending, []],
    ["same hash seen keeps reviewing", reviewing, { tag: "seen", id: "p1", hash: "h1", pending: true }, reviewing, []],
    ["new hash seen means changed", reviewing, { tag: "seen", id: "p1", hash: "h2", pending: true }, { tag: "changed", id: "p1" }, []],
    ["decided elsewhere closes", reviewing, { tag: "seen", id: "p1", hash: "h1", pending: false }, idle, []],
    ["other proposal seen is ignored", reviewing, { tag: "seen", id: "p9", hash: "x", pending: true }, reviewing, []],
    ["409 reloads and says changed", sending, { tag: "conflict" }, { tag: "changed", id: "p1" }, ["reload"]],
    ["other error is shown", sending, { tag: "failed", message: "boom" }, { tag: "failed", id: "p1", message: "boom" }, []],
    ["success closes", sending, { tag: "succeeded" }, idle, []],
    ["cancel while sending is ignored", sending, { tag: "cancel" }, sending, []],
    ["cancel closes", reviewing, { tag: "cancel" }, idle, []],
    ["review again after changed", { tag: "changed", id: "p1" }, { tag: "open", id: "p1", hash: "h2" }, { tag: "reviewing", id: "p1", hash: "h2" }, []],
  ];
  it.each(table)("%s", (_n, state, input, want, cmds) => {
    const [next, commands] = stepConfirm(state, input);
    expect(next).toEqual(want);
    expect(commands.map((c) => (c.tag === "send" ? `send ${c.id} ${c.hash}` : c.tag))).toEqual(cmds);
  });
});

describe("describeProposal", () => {
  const table: [string, Parameters<typeof describeProposal>[0], string, string[]][] = [
    ["pending", { status: "pending" }, "Waiting for you", ["confirm", "reject"]],
    ["confirmed", { status: "confirmed" }, "Running", []],
    ["executed", { status: "executed" }, "Done", []],
    ["rejected", { status: "rejected" }, "Rejected", []],
    ["stale retryable", { status: "failed", failKind: "stale", retryable: true }, "Stale", ["retry"]],
    ["interrupted retryable", { status: "failed", failKind: "interrupted", retryable: true }, "Interrupted", ["retry"]],
    ["failed not retryable", { status: "failed", failKind: "exec_error", message: "no" }, "Failed", []],
  ];
  it.each(table)("%s", (_n, p, label, actions) => {
    const v = describeProposal(p);
    expect(v.label).toBe(label);
    expect(v.actions).toEqual(actions);
  });
});

describe("groupByChangeSet", () => {
  const p = (id: string, cs?: string) => ({ id, changeSetId: cs }) as ProposalDTO;
  it("groups a change set together and keeps others alone, in order", () => {
    const g = groupByChangeSet([p("a", "cs1"), p("b"), p("c", "cs1"), p("d", "cs2")]);
    expect(g.map((x) => x.items.map((i) => i.id))).toEqual([["a", "c"], ["b"], ["d"]]);
    expect(g.map((x) => x.changeSetId)).toEqual(["cs1", undefined, "cs2"]);
  });
});
