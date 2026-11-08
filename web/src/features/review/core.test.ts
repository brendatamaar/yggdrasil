import { describe, expect, it } from "vitest";
import { mixedDiff } from "./fixtures";
import { closedEditor, diffHash, parseFileDiffs, stepNote, type Anchor, type NoteEditor } from "./core";

const anchor: Anchor = { repoId: "r1", path: "src/a.ts", line: 2, side: "new" };


describe("parseFileDiffs", () => {
  it("flags hunks that only change line endings and keeps the real change", () => {
    const [f] = parseFileDiffs(mixedDiff);
    expect(f?.path).toBe("src/a.ts");
    expect(f?.hunks.map((h) => h.eolOnly)).toEqual([true, false]);
    expect(f?.eolChanged).toBe(true);
  });

  it("does not flag a real edit as line-endings-only", () => {
    const [f] = parseFileDiffs("diff --git a/x b/x\n@@ -1 +1 @@\n-a\n+b\n");
    expect(f?.eolChanged).toBe(false);
  });

  it("splits files", () => {
    const text = "diff --git a/x b/x\n@@ -1 +1 @@\n-a\n+b\ndiff --git a/y b/y\n@@ -1 +1 @@\n-c\n+d\n";
    expect(parseFileDiffs(text).map((f) => f.path)).toEqual(["x", "y"]);
  });
});

describe("diffHash", () => {
  it("is 64 hex characters", async () => {
    expect(await diffHash("abc")).toMatch(/^[0-9a-f]{64}$/);
  });
});

describe("stepNote", () => {
  it("opens, types and submits one save command", () => {
    let s: NoteEditor = closedEditor;
    s = stepNote(s, { type: "open", anchor, text: "" })[0];
    s = stepNote(s, { type: "type", text: " fix this " })[0];
    const [saving, cmds] = stepNote(s, { type: "submit" });
    expect(saving.kind).toBe("saving");
    expect(cmds).toEqual([{ type: "save", anchor, noteId: undefined, text: "fix this" }]);
  });

  it("ignores an empty submit and a second submit while saving", () => {
    const [s0] = stepNote(closedEditor, { type: "open", anchor, text: "  " });
    expect(stepNote(s0, { type: "submit" })[1]).toEqual([]);
    const [s1] = stepNote({ kind: "editing", anchor, text: "x" }, { type: "submit" });
    expect(stepNote(s1, { type: "submit" })).toEqual([s1, []]);
  });

  it("returns to editing with the error when the save fails", () => {
    const saving: NoteEditor = { kind: "saving", anchor, text: "x" };
    const [s] = stepNote(saving, { type: "failed", message: "no" });
    expect(s).toMatchObject({ kind: "editing", text: "x", error: "no" });
  });
});
