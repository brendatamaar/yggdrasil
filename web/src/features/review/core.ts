import type { ReviewNoteDTO, VerificationDTO } from "../../api/gen/api.gen";

// ---- Verification badges -------------------------------------------------

export interface BadgeInfo {
  label: string;
  tone: "ok" | "danger" | "warn" | "muted" | "busy";
}

const badges: Record<string, BadgeInfo> = {
  none: { label: "Not verified", tone: "muted" },
  skipped: { label: "Skipped", tone: "muted" },
  unverifiable: { label: "Can't verify", tone: "warn" },
  bootstrapping: { label: "Setting up", tone: "busy" },
  verifying: { label: "Verifying", tone: "busy" },
  passed: { label: "Passed", tone: "ok" },
  failed: { label: "Failed", tone: "danger" },
  interrupted: { label: "Interrupted", tone: "warn" },
  cancelled: { label: "Cancelled", tone: "muted" },
};

/** Label and tone of a verification state; an unknown state shows as itself. */
export function badgeFor(state: string): BadgeInfo {
  return badges[state] ?? { label: state, tone: "muted" };
}

export const isRunning = (v: VerificationDTO) => v.state === "bootstrapping" || v.state === "verifying";

// ---- Diff parsing --------------------------------------------------------

export interface DiffRow {
  kind: "add" | "del" | "ctx";
  text: string;
  oldNo?: number;
  newNo?: number;
}

export interface Hunk {
  header: string;
  rows: DiffRow[];
  /** True when the hunk only changes line endings. */
  eolOnly: boolean;
}

export interface FileDiff {
  path: string;
  /** The file's diff text exactly as the server sent it (what a note's hash covers). */
  text: string;
  hunks: Hunk[];
  /** At least one hunk changes line endings only (shown as a chip). */
  eolChanged: boolean;
}

const hunkRe = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/;
const noCR = (s: string) => (s.endsWith("\r") ? s.slice(0, -1) : s);

function isEolOnly(rows: DiffRow[]): boolean {
  const dels = rows.filter((r) => r.kind === "del");
  const adds = rows.filter((r) => r.kind === "add");
  if (dels.length === 0 || dels.length !== adds.length) return false;
  let differs = false;
  for (let i = 0; i < dels.length; i++) {
    const d = dels[i]?.text ?? "";
    const a = adds[i]?.text ?? "";
    if (noCR(d) !== noCR(a)) return false;
    if (d !== a) differs = true;
  }
  return differs;
}

function parseFile(text: string): FileDiff {
  const lines = text.split("\n");
  if (lines[lines.length - 1] === "") lines.pop();
  const m = /^diff --git a\/(.+) b\/(.+)$/.exec(noCR(lines[0] ?? ""));
  let path = m?.[2] ?? "";
  const hunks: Hunk[] = [];
  let cur: Hunk | undefined;
  let o = 0;
  let n = 0;
  for (const raw of lines) {
    const h = hunkRe.exec(raw);
    if (h) {
      o = Number(h[1]);
      n = Number(h[2]);
      cur = { header: noCR(raw), rows: [], eolOnly: false };
      hunks.push(cur);
    } else if (!cur) {
      if (raw.startsWith("+++ b/")) path = noCR(raw.slice(6));
    } else if (raw.startsWith("\\")) {
      continue;
    } else if (raw.startsWith("+")) {
      cur.rows.push({ kind: "add", text: raw.slice(1), newNo: n++ });
    } else if (raw.startsWith("-")) {
      cur.rows.push({ kind: "del", text: raw.slice(1), oldNo: o++ });
    } else {
      cur.rows.push({ kind: "ctx", text: raw.slice(1), oldNo: o++, newNo: n++ });
    }
  }
  for (const hk of hunks) hk.eolOnly = isEolOnly(hk.rows);
  return { path, text, hunks, eolChanged: hunks.some((x) => x.eolOnly) };
}

/** Splits a multi-file unified diff into files, flagging hunks that only change line endings (PL-03). */
export function parseFileDiffs(text: string): FileDiff[] {
  if (text === "") return [];
  const parts = text.split(/^(?=diff --git )/m).filter((p) => p.trim() !== "");
  return parts.map(parseFile);
}

/** Hex SHA-256 of a file's diff text: the diffHash a note is created with. */
export async function diffHash(text: string): Promise<string> {
  const buf = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text));
  return Array.from(new Uint8Array(buf), (b) => b.toString(16).padStart(2, "0")).join("");
}

// ---- Notes ---------------------------------------------------------------

export interface Anchor {
  repoId: string;
  path: string;
  line: number;
  side: "old" | "new";
}

export const sameAnchor = (n: Pick<ReviewNoteDTO, "repoId" | "path" | "line" | "side">, a: Anchor) =>
  n.repoId === a.repoId && n.path === a.path && n.line === a.line && n.side === a.side;

/** Notes that "Send notes to agent" would send: the open ones. */
export const sendable = (notes: ReviewNoteDTO[]) => notes.filter((n) => n.state === "open");

// ---- Note editor state machine (docs/AGENTS.md 5.11) ---------------------

export type NoteEditor =
  | { kind: "closed" }
  | { kind: "editing"; anchor: Anchor; noteId?: string; text: string; error?: string }
  | { kind: "saving"; anchor: Anchor; noteId?: string; text: string };

export type NoteInput =
  | { type: "open"; anchor: Anchor; noteId?: string; text: string }
  | { type: "type"; text: string }
  | { type: "submit" }
  | { type: "saved" }
  | { type: "failed"; message: string }
  | { type: "cancel" };

export interface SaveCommand {
  type: "save";
  anchor: Anchor;
  noteId?: string;
  text: string;
}

export const closedEditor: NoteEditor = { kind: "closed" };

/** (state, input) -> (state, commands). Unhandled inputs return the state unchanged. */
export function stepNote(s: NoteEditor, i: NoteInput): [NoteEditor, SaveCommand[]] {
  switch (i.type) {
    case "open":
      if (s.kind === "saving") return [s, []];
      return [{ kind: "editing", anchor: i.anchor, noteId: i.noteId, text: i.text }, []];
    case "type":
      if (s.kind !== "editing") return [s, []];
      return [{ kind: "editing", anchor: s.anchor, noteId: s.noteId, text: i.text }, []];
    case "submit": {
      if (s.kind !== "editing" || s.text.trim() === "") return [s, []];
      const text = s.text.trim();
      return [
        { kind: "saving", anchor: s.anchor, noteId: s.noteId, text },
        [{ type: "save", anchor: s.anchor, noteId: s.noteId, text }],
      ];
    }
    case "saved":
      return s.kind === "saving" ? [closedEditor, []] : [s, []];
    case "failed":
      return s.kind === "saving"
        ? [{ kind: "editing", anchor: s.anchor, noteId: s.noteId, text: s.text, error: i.message }, []]
        : [s, []];
    case "cancel":
      return s.kind === "saving" ? [s, []] : [closedEditor, []];
  }
}
