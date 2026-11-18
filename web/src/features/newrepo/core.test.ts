import { describe, expect, it } from "vitest";
import type { NewRepoOperationDTO } from "../../api/gen/api.gen";
import {
  describeOperation,
  findPublishProposal,
  initRequest,
  leafProblem,
  retrySpecOf,
  stepPrepare,
  stepRetry,
  type PrepareInput,
  type PrepareState,
  type RetryState,
} from "./core";

interface Form {
  leaf: string;
}
interface Preview {
  hash: string;
}
type S = PrepareState<Form, Preview, { id: string }>;
type I = PrepareInput<Form, Preview, { id: string }>;
const step = (s: S, i: I) => stepPrepare(s, i);
const form: Form = { leaf: "shop-docs" };
const preview: Preview = { hash: "a".repeat(64) };

const previewing: S = { tag: "previewing", requestId: "r1", form };
const previewed: S = { tag: "previewed", requestId: "r1", form, preview };
const submitting: S = { tag: "submitting", requestId: "r1", form, preview };

describe("prepare flow", () => {
  it("preview from editing, previewed or failed starts a query", () => {
    for (const from of [{ tag: "editing" }, previewed, { tag: "failed", message: "x", retry: { tag: "preview", form } }] as S[]) {
      const [s, cmds] = step(from, { tag: "preview", form, requestId: "r2" });
      expect(s).toEqual({ tag: "previewing", requestId: "r2", form });
      expect(cmds).toEqual([{ tag: "queryPreview", requestId: "r2", form }]);
    }
  });

  it("a new preview while one is loading cancels the old query", () => {
    const [s, cmds] = step(previewing, { tag: "preview", form, requestId: "r2" });
    expect(s.tag).toBe("previewing");
    expect(cmds).toEqual([
      { tag: "cancelPreview", requestId: "r1" },
      { tag: "queryPreview", requestId: "r2", form },
    ]);
  });

  it("a preview is ignored while submitting or after it created something", () => {
    for (const from of [submitting, { tag: "created", result: { id: "o1" }, preview }] as S[]) {
      expect(step(from, { tag: "preview", form, requestId: "r2" })).toEqual([from, []]);
    }
  });

  it("previewLoaded for the current request shows the preview; a stale one is ignored", () => {
    expect(step(previewing, { tag: "previewLoaded", requestId: "r1", preview })[0]).toEqual(previewed);
    expect(step(previewing, { tag: "previewLoaded", requestId: "old", preview })).toEqual([previewing, []]);
    expect(step(previewed, { tag: "previewLoaded", requestId: "r1", preview })).toEqual([previewed, []]);
  });

  it("previewFailed for the current request fails with a preview retry; a stale one is ignored", () => {
    const [s] = step(previewing, { tag: "previewFailed", requestId: "r1", message: "no" });
    expect(s).toEqual({ tag: "failed", message: "no", retry: { tag: "preview", form } });
    expect(step(previewing, { tag: "previewFailed", requestId: "old", message: "no" })).toEqual([previewing, []]);
  });

  it("submit sends the SAME request ID and exactly the hash of the preview that was shown", () => {
    const [s, cmds] = step(previewed, { tag: "submit" });
    expect(s).toEqual(submitting);
    expect(cmds).toEqual([{ tag: "submit", requestId: "r1", form, hash: preview.hash }]);
  });

  it("submit is ignored unless a preview is showing", () => {
    for (const from of [{ tag: "editing" }, previewing, submitting] as S[]) expect(step(from, { tag: "submit" })).toEqual([from, []]);
  });

  it("submitted for the current request is created; a stale answer is ignored", () => {
    expect(step(submitting, { tag: "submitted", requestId: "r1", result: { id: "o1" } })[0]).toEqual({
      tag: "created",
      result: { id: "o1" },
      preview,
    });
    expect(step(submitting, { tag: "submitted", requestId: "old", result: { id: "o1" } })).toEqual([submitting, []]);
    expect(step(previewed, { tag: "submitted", requestId: "r1", result: { id: "o1" } })).toEqual([previewed, []]);
  });

  it("a refusal retries from a fresh preview; an unknown outcome retries the same request", () => {
    const [refused] = step(submitting, { tag: "submitFailed", requestId: "r1", message: "taken", definite: true });
    expect(refused).toMatchObject({ tag: "failed", retry: { tag: "preview", form } });
    const [unknown] = step(submitting, { tag: "submitFailed", requestId: "r1", message: "lost", definite: false });
    expect(unknown).toMatchObject({ tag: "failed", retry: { tag: "submit", requestId: "r1", preview } });
    expect(step(submitting, { tag: "submitFailed", requestId: "old", message: "x", definite: false })).toEqual([submitting, []]);
  });

  it("retry after an unknown outcome repeats the same request ID and hash, never a new one", () => {
    const [unknown] = step(submitting, { tag: "submitFailed", requestId: "r1", message: "lost", definite: false });
    const [s, cmds] = step(unknown, { tag: "retry", requestId: "fresh" });
    expect(s).toEqual(submitting);
    expect(cmds).toEqual([{ tag: "submit", requestId: "r1", form, hash: preview.hash }]);
  });

  it("retry after a refusal starts a new preview with the new request ID", () => {
    const [refused] = step(submitting, { tag: "submitFailed", requestId: "r1", message: "taken", definite: true });
    const [s, cmds] = step(refused, { tag: "retry", requestId: "fresh" });
    expect(s).toEqual({ tag: "previewing", requestId: "fresh", form });
    expect(cmds).toEqual([{ tag: "queryPreview", requestId: "fresh", form }]);
  });

  it("retry is ignored unless failed", () => {
    expect(step(previewed, { tag: "retry", requestId: "x" })).toEqual([previewed, []]);
  });

  it("edit returns to the form (cancelling a loading preview); reset does too", () => {
    expect(step(previewed, { tag: "edit" })).toEqual([{ tag: "editing" }, []]);
    expect(step(previewing, { tag: "edit" })).toEqual([{ tag: "editing" }, [{ tag: "cancelPreview", requestId: "r1" }]]);
    expect(step(submitting, { tag: "edit" })).toEqual([submitting, []]);
    expect(step(previewing, { tag: "reset" })[1]).toEqual([{ tag: "cancelPreview", requestId: "r1" }]);
    expect(step(submitting, { tag: "reset" })[0]).toEqual({ tag: "editing" });
  });
});

describe("retry confirmation", () => {
  const spec = { hash: "b".repeat(64), lines: ["Push the first commit"] };
  const reviewing: RetryState = { tag: "reviewing", ...spec };
  const sending: RetryState = { tag: "sending", ...spec };

  it("open shows the remembered hash, or says it is missing", () => {
    expect(stepRetry({ tag: "closed" }, { tag: "open", spec })[0]).toEqual(reviewing);
    expect(stepRetry({ tag: "closed" }, { tag: "open", spec: undefined })[0]).toEqual({ tag: "missing" });
    expect(stepRetry(sending, { tag: "open", spec })).toEqual([sending, []]);
  });

  it("confirm sends exactly the hash that was shown; nothing is sent from any other state", () => {
    expect(stepRetry(reviewing, { tag: "confirm" })).toEqual([sending, [{ tag: "sendRetry", hash: spec.hash }]]);
    for (const s of [{ tag: "closed" }, { tag: "missing" }, sending] as RetryState[]) expect(stepRetry(s, { tag: "confirm" })).toEqual([s, []]);
  });

  it("success closes and refreshes; failure keeps the dialog, refreshes and allows another explicit confirm", () => {
    expect(stepRetry(sending, { tag: "succeeded" })).toEqual([{ tag: "closed" }, [{ tag: "refresh" }]]);
    const [failed, cmds] = stepRetry(sending, { tag: "failed", message: "changed" });
    expect(failed).toEqual({ tag: "failed", ...spec, message: "changed" });
    expect(cmds).toEqual([{ tag: "refresh" }]);
    expect(stepRetry(failed, { tag: "confirm" })[1]).toEqual([{ tag: "sendRetry", hash: spec.hash }]);
    expect(stepRetry(reviewing, { tag: "succeeded" })).toEqual([reviewing, []]); // stale result
  });

  it("close is ignored while sending", () => {
    expect(stepRetry(sending, { tag: "close" })).toEqual([sending, []]);
    expect(stepRetry(reviewing, { tag: "close" })[0]).toEqual({ tag: "closed" });
  });
});

const op = (over: Partial<NewRepoOperationDTO>): NewRepoOperationDTO => ({
  operationId: "o1",
  projectId: "p1",
  repoId: "r1",
  setupTaskId: "t1",
  leaf: "shop-docs",
  state: "ready",
  ...over,
});

describe("operation wording", () => {
  it("renders each backend variant by its own name", () => {
    const states = ["reserved", "awaiting_confirm", "initializing", "ready", "publishing", "published", "uncertain", "stopped"];
    expect(states.map((s) => describeOperation(op({ state: s })).variant)).toEqual(states);
  });

  it("an unknown outcome is never worded as success or as a plain failure", () => {
    const v = describeOperation(
      op({ state: "uncertain", stages: [{ name: "create_remote", status: "unknown" }], reason: "The host did not answer.", canRetry: true }),
    );
    expect(v.tone).toBe("unknown");
    expect(v.headline).toBe("Outcome unknown");
    expect(v.detail).toContain("may or may not exist");
    expect(v.detail).toContain("repeats nothing on its own");
    expect(v.headline).not.toMatch(/published|done|ready|failed/i);
    expect(v.next).toEqual({ tag: "retry", kind: "publish" });
  });

  it("a stopped operation says what stopped and that nothing was deleted", () => {
    const v = describeOperation(op({ state: "stopped", stages: [{ name: "write_readme", status: "failed" }], reason: "The file is locked.", canRetry: true }));
    expect(v.tone).toBe("problem");
    expect(v.detail).toContain("write the readme");
    expect(v.detail).toContain("nothing was deleted");
    expect(v.next).toEqual({ tag: "retry", kind: "init" });
  });

  it("offers retry only when the server says it can", () => {
    expect(describeOperation(op({ state: "stopped", canRetry: false })).next).toEqual({ tag: "none" });
  });

  it("awaiting_confirm leads to the proposal; ready leads to publish; published shows the credential-free remote", () => {
    expect(describeOperation(op({ state: "awaiting_confirm", proposalId: "p9" })).next).toEqual({ tag: "confirm", proposalId: "p9" });
    expect(describeOperation(op({ state: "ready" })).next).toEqual({ tag: "publish" });
    expect(describeOperation(op({ state: "published", remoteUrl: "https://git.acme.dev/acme-shop/shop-docs" })).remoteUrl).toBe(
      "https://git.acme.dev/acme-shop/shop-docs",
    );
  });

  it("an unknown variant is called out, not guessed", () => {
    expect(describeOperation(op({ state: "bogus" })).headline).toBe("Unknown state");
  });
});

describe("form helpers", () => {
  it("leaves empty optional text out so the server defaults apply", () => {
    expect(initRequest({ leaf: " shop-docs ", readme: "", ignore: "", commitMessage: " " })).toEqual({ leaf: "shop-docs" });
    expect(initRequest({ leaf: "a", readme: "# x", ignore: "", commitMessage: "init" })).toEqual({ leaf: "a", readme: "# x", commitMessage: "init" });
  });
  it("a leaf is one folder name", () => {
    expect(leafProblem("")).toBeDefined();
    expect(leafProblem("a/b")).toBeDefined();
    expect(leafProblem("a\\b")).toBeDefined();
    expect(leafProblem("shop-docs")).toBeUndefined();
  });
});

describe("retrySpecOf", () => {
  const hash = "a".repeat(64);
  it("takes the hash from the server's operation, not from the session", () => {
    expect(retrySpecOf({ specHash: hash }, { hash: "b".repeat(64), lines: ["old"] })).toEqual({ hash, lines: [] });
    expect(retrySpecOf({ specHash: hash }, undefined)).toEqual({ hash, lines: [] });
  });
  it("keeps the remembered wording only for the same hash", () => {
    expect(retrySpecOf({ specHash: hash }, { hash, lines: ["Write README.md"] })).toEqual({ hash, lines: ["Write README.md"] });
  });
  it("has no spec without a server hash, even with a remembered preview", () => {
    expect(retrySpecOf({}, { hash, lines: ["x"] })).toBeUndefined();
    expect(retrySpecOf({ specHash: "" }, { hash, lines: ["x"] })).toBeUndefined();
  });
});

describe("findPublishProposal", () => {
  const p = (id: string, status: string, createdAt: string, kind = "publish") => ({ id, kind, status, createdAt });
  const list = [p("init", "executed", "2026-01-01", "init_repo"), p("old", "rejected", "2026-01-02"), p("a", "pending", "2026-01-03"), p("b", "pending", "2026-01-04")];
  it("finds the newest pending publish proposal while ready", () => {
    expect(findPublishProposal({ state: "ready" }, list)).toBe("b");
  });
  it("ignores other kinds and finished ones while ready", () => {
    expect(findPublishProposal({ state: "ready" }, [p("init", "pending", "2026-01-01", "init_repo"), p("x", "executed", "2026-01-02")])).toBeUndefined();
  });
  it("keeps a confirmed one in view while publishing, and looks nowhere in other states", () => {
    expect(findPublishProposal({ state: "publishing" }, [p("c", "confirmed", "2026-01-05")])).toBe("c");
    expect(findPublishProposal({ state: "published" }, list)).toBeUndefined();
    expect(findPublishProposal({ state: "awaiting_confirm" }, list)).toBeUndefined();
  });
});
