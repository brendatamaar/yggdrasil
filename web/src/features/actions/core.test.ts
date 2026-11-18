import { describe, expect, it } from "vitest";
import type { ActionPreviewDTO, ActionResultDTO } from "../../api/gen/api.gen";
import { stepAction, type ActionInput, type ActionState, type ActionTarget } from "./core";

const target: ActionTarget = { repoId: "r1", kind: "pull_and_check", title: "Pull and check", repoName: "shop-api" };
const other: ActionTarget = { repoId: "r1", kind: "diagnose_repo", title: "Diagnose repo" };
const preview = (requestId: string, over: Partial<ActionPreviewDTO> = {}): ActionPreviewDTO => ({
  requestId,
  digest: `digest-${requestId}`,
  kind: "pull_and_check",
  source: "shop-api main",
  contextText: "behind by 2",
  context: "available",
  expectedHead: "abc12345",
  ...over,
});
const result = (requestId: string, state: ActionResultDTO["state"], over: Partial<ActionResultDTO> = {}): ActionResultDTO => ({
  requestId,
  state,
  message: state === "completed" ? "Created a pull proposal." : "Refused.",
  ...over,
});

const closed: ActionState = { tag: "closed" };
const loading = (id = "q1"): ActionState => ({ tag: "loading", requestId: id, target });
const inPreview = (id = "q1"): ActionState => ({ tag: "preview", requestId: id, target, preview: preview(id) });
const submitting = (id = "q1"): ActionState => ({
  tag: "submitting",
  target,
  requestId: id,
  digest: `digest-${id}`,
  expectedHead: "abc12345",
});

describe("action dialog reducer", () => {
  it("Closed + open -> Loading with one preview query", () => {
    const [s, c] = stepAction(closed, { tag: "open", target, requestId: "q1" });
    expect(s).toEqual(loading());
    expect(c).toEqual([{ tag: "queryPreview", request: { requestId: "q1", repoId: "r1", kind: "pull_and_check" } }]);
  });

  it("open carries the source and the agent into the request", () => {
    const [, c] = stepAction(closed, { tag: "open", target: { ...target, sourceId: "42", agent: "mock" }, requestId: "q1" });
    expect(c[0]).toMatchObject({ request: { sourceId: "42", agent: "mock" } });
  });

  it("Failed + open and Succeeded + open start a fresh preview", () => {
    const failed: ActionState = { tag: "failed", message: "x", retry: { tag: "preview", target } };
    expect(stepAction(failed, { tag: "open", target, requestId: "q2" })[0]).toEqual(loading("q2"));
    const done: ActionState = { tag: "succeeded", requestId: "q1", target, result: result("q1", "completed") };
    expect(stepAction(done, { tag: "open", target, requestId: "q2" })[0]).toEqual(loading("q2"));
  });

  it("open while Loading cancels the old query first", () => {
    const [s, c] = stepAction(loading("q1"), { tag: "open", target: other, requestId: "q2" });
    expect(s).toMatchObject({ tag: "loading", requestId: "q2", target: other });
    expect(c.map((x) => x.tag)).toEqual(["cancelQuery", "queryPreview"]);
  });

  it("open while Submitting changes nothing", () => {
    const s = submitting();
    expect(stepAction(s, { tag: "open", target: other, requestId: "q9" })).toEqual([s, []]);
  });

  it("Loading + loaded (matching) -> Preview, no commands", () => {
    expect(stepAction(loading(), { tag: "loaded", requestId: "q1", preview: preview("q1") })).toEqual([inPreview(), []]);
  });

  it("Loading + loaded for another request is ignored (stale)", () => {
    const s = loading("q2");
    expect(stepAction(s, { tag: "loaded", requestId: "q1", preview: preview("q1") })).toEqual([s, []]);
  });

  it("a preview that names another request fails instead of being shown", () => {
    const [s] = stepAction(loading(), { tag: "loaded", requestId: "q1", preview: preview("other") });
    expect(s.tag).toBe("failed");
  });

  it("Loading + loadFailed -> Failed with a preview retry", () => {
    const [s] = stepAction(loading(), { tag: "loadFailed", requestId: "q1", message: "Offline." });
    expect(s).toEqual({ tag: "failed", message: "Offline.", retry: { tag: "preview", target } });
  });

  it("a late loadFailed for an old request is ignored", () => {
    const s = loading("q2");
    expect(stepAction(s, { tag: "loadFailed", requestId: "q1", message: "x" })).toEqual([s, []]);
  });

  it("Preview + submit -> Submitting with the SAME request ID and the preview digest and head", () => {
    const [s, c] = stepAction(inPreview(), { tag: "submit" });
    expect(s).toEqual(submitting());
    expect(c).toEqual([
      {
        tag: "submitAction",
        request: { requestId: "q1", repoId: "r1", kind: "pull_and_check" },
        digest: "digest-q1",
        expectedHead: "abc12345",
      },
    ]);
  });

  it("submit is ignored outside Preview (no double submit)", () => {
    for (const s of [closed, loading(), submitting(), { tag: "failed", message: "", retry: { tag: "preview", target } } as ActionState]) {
      expect(stepAction(s, { tag: "submit" })).toEqual([s, []]);
    }
  });

  it("Submitting + submitted completed -> Succeeded and invalidates", () => {
    const r = result("q1", "completed", { proposalIds: ["p1"] });
    const [s, c] = stepAction(submitting(), { tag: "submitted", requestId: "q1", result: r });
    expect(s).toEqual({ tag: "succeeded", requestId: "q1", target, result: r });
    expect(c).toEqual([{ tag: "invalidate" }]);
  });

  it("Submitting + submitted refused -> Failed with a fresh-preview retry", () => {
    const [s] = stepAction(submitting(), { tag: "submitted", requestId: "q1", result: result("q1", "refused", { message: "The head changed." }) });
    expect(s).toEqual({ tag: "failed", message: "The head changed.", retry: { tag: "preview", target } });
  });

  it("Submitting + submitted 'submitting' stays and reconciles", () => {
    const s = submitting();
    expect(stepAction(s, { tag: "submitted", requestId: "q1", result: result("q1", "submitting") })).toEqual([
      s,
      [{ tag: "reconcile", requestId: "q1" }],
    ]);
  });

  it("a submitted result for another request is ignored (stale)", () => {
    const s = submitting("q2");
    expect(stepAction(s, { tag: "submitted", requestId: "q1", result: result("q1", "completed") })).toEqual([s, []]);
  });

  it("submitted outside Submitting is ignored", () => {
    for (const s of [closed, loading(), inPreview()]) {
      expect(stepAction(s, { tag: "submitted", requestId: "q1", result: result("q1", "completed") })).toEqual([s, []]);
    }
  });

  it("a definite refusal -> Failed with a preview retry; an unknown outcome keeps the key for a same-request retry", () => {
    const [def] = stepAction(submitting(), { tag: "submitFailed", requestId: "q1", message: "Refused", definite: true });
    expect(def).toMatchObject({ tag: "failed", retry: { tag: "preview" } });
    const [unk] = stepAction(submitting(), { tag: "submitFailed", requestId: "q1", message: "Offline", definite: false });
    expect(unk).toMatchObject({ tag: "failed", retry: { tag: "submit", requestId: "q1", digest: "digest-q1" } });
    const [again, c] = stepAction(unk, { tag: "retry", requestId: "fresh" });
    expect(again).toEqual(submitting("q1"));
    expect(c[0]).toMatchObject({ tag: "submitAction", request: { requestId: "q1" }, digest: "digest-q1" });
  });

  it("a stale submitFailed is ignored", () => {
    const s = submitting("q2");
    expect(stepAction(s, { tag: "submitFailed", requestId: "q1", message: "x", definite: true })).toEqual([s, []]);
  });

  it("retry after a load failure uses the new request ID", () => {
    const failed: ActionState = { tag: "failed", message: "x", retry: { tag: "preview", target } };
    const [s, c] = stepAction(failed, { tag: "retry", requestId: "q7" });
    expect(s).toEqual(loading("q7"));
    expect(c[0]).toMatchObject({ tag: "queryPreview", request: { requestId: "q7" } });
  });

  it("retry outside Failed is ignored", () => {
    expect(stepAction(inPreview(), { tag: "retry", requestId: "q7" })).toEqual([inPreview(), []]);
  });

  it("close cancels a pending preview query and closes", () => {
    expect(stepAction(loading(), { tag: "close" })).toEqual([closed, [{ tag: "cancelQuery", requestId: "q1" }]]);
    expect(stepAction(inPreview(), { tag: "close" })).toEqual([closed, []]);
    expect(stepAction(closed, { tag: "close" })).toEqual([closed, []]);
  });

  it("closing while Submitting never cancels it and keeps the key", () => {
    const [s, c] = stepAction(submitting(), { tag: "close" });
    expect(c).toEqual([]);
    expect(s).toMatchObject({ tag: "closed", inFlight: { requestId: "q1", digest: "digest-q1" } });
  });

  it("reopening the same action after close reconciles the SAME request, never a second one", () => {
    const [closedState] = stepAction(submitting(), { tag: "close" });
    const [s, c] = stepAction(closedState, { tag: "open", target, requestId: "brand-new" });
    expect(s).toEqual(submitting("q1"));
    expect(c).toHaveLength(1);
    expect(c[0]).toMatchObject({ tag: "submitAction", request: { requestId: "q1" }, digest: "digest-q1" });
  });

  it("reopening a different action starts its own preview", () => {
    const [closedState] = stepAction(submitting(), { tag: "close" });
    expect(stepAction(closedState, { tag: "open", target: other, requestId: "q5" })[0]).toEqual({ tag: "loading", requestId: "q5", target: other });
  });

  it("a result that arrives after close clears the key and refreshes", () => {
    const [closedState] = stepAction(submitting(), { tag: "close" });
    const [s, c] = stepAction(closedState, { tag: "submitted", requestId: "q1", result: result("q1", "completed") });
    expect(s).toEqual(closed);
    expect(c).toEqual([{ tag: "invalidate" }]);
  });

  it("an unknown outcome after close keeps the key; a definite refusal frees it", () => {
    const [closedState] = stepAction(submitting(), { tag: "close" });
    expect(stepAction(closedState, { tag: "submitFailed", requestId: "q1", message: "x", definite: false })[0]).toEqual(closedState);
    expect(stepAction(closedState, { tag: "submitFailed", requestId: "q1", message: "x", definite: true })[0]).toEqual(closed);
  });

  it("closing a Failed dialog after an unknown outcome keeps the key", () => {
    const [unk] = stepAction(submitting(), { tag: "submitFailed", requestId: "q1", message: "x", definite: false });
    expect(stepAction(unk, { tag: "close" })[0]).toMatchObject({ tag: "closed", inFlight: { requestId: "q1" } });
  });
});

describe("action dialog reducer, over random input sequences", () => {
  // A seeded generator: the same sequences on every run.
  function rng(seed: number) {
    let s = seed;
    return () => (s = (s * 1103515245 + 12345) % 2147483648) / 2147483648;
  }
  const ids = ["a", "b"];

  it("never produces Succeeded unless that exact RequestID was Submitting and answered", () => {
    let succeeded = 0;
    for (let seed = 1; seed <= 200; seed++) {
      const r = rng(seed);
      const pick = <T,>(xs: readonly T[]): T => xs[Math.floor(r() * xs.length)] as T;
      let state: ActionState = closed;
      const submittedIds = new Set<string>();
      for (let i = 0; i < 60; i++) {
        const id = pick(ids);
        const inputs: ActionInput[] = [
          { tag: "open", target: pick([target, other]), requestId: pick(ids) },
          { tag: "loaded", requestId: id, preview: preview(id) },
          { tag: "loadFailed", requestId: id, message: "x" },
          { tag: "submit" },
          { tag: "submitted", requestId: id, result: result(id, pick(["completed", "refused", "submitting", "cancelled"] as const)) },
          { tag: "submitFailed", requestId: id, message: "x", definite: r() < 0.5 },
          { tag: "retry", requestId: pick(ids) },
          { tag: "close" },
        ];
        // Half the time follow the happy path of the current state, so deep states are reached.
        let input: ActionInput = pick(inputs);
        if (r() < 0.5) {
          if (state.tag === "closed" || state.tag === "failed" || state.tag === "succeeded") input = { tag: "open", target, requestId: id };
          else if (state.tag === "loading") input = { tag: "loaded", requestId: state.requestId, preview: preview(state.requestId) };
          else if (state.tag === "preview") input = { tag: "submit" };
          else input = { tag: "submitted", requestId: state.requestId, result: result(state.requestId, pick(["completed", "refused", "submitting"] as const)) };
        }
        const before: ActionState = state;
        const [after, cmds] = stepAction(state, input);
        state = after;
        if (before.tag === "submitting") submittedIds.add(before.requestId);
        if (cmds.some((c) => c.tag === "submitAction")) {
          // Every submission carries the digest of the preview that was shown.
          const c = cmds.find((x) => x.tag === "submitAction");
          if (c?.tag === "submitAction") expect(c.digest).toBe(`digest-${c.request.requestId}`);
        }
        if (state.tag === "succeeded" && state !== before) {
          succeeded++;
          expect(before.tag).toBe("submitting");
          expect(input.tag).toBe("submitted");
          if (before.tag === "submitting" && input.tag === "submitted") {
            expect(input.requestId).toBe(before.requestId);
            expect(state.requestId).toBe(before.requestId);
            expect(submittedIds.has(state.requestId)).toBe(true);
          }
        }
      }
    }
    // The generator reaches the interesting state, so the property is not vacuous.
    expect(succeeded).toBeGreaterThan(20);
  });
});
