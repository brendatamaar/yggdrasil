export type DiffLineKind = "add" | "del" | "ctx" | "hunk" | "meta";

export interface DiffLine {
  kind: DiffLineKind;
  text: string;
  oldNo?: number;
  newNo?: number;
}

const hunkRe = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/;

/** Splits unified-diff text into lines with old/new line numbers. */
export function parseDiff(text: string): DiffLine[] {
  if (text === "") return [];
  const out: DiffLine[] = [];
  let o = 0;
  let n = 0;
  let inHunk = false;
  const lines = text.split("\n");
  if (lines[lines.length - 1] === "") lines.pop();
  for (const raw of lines) {
    const line = raw.endsWith("\r") ? raw.slice(0, -1) : raw;
    const m = hunkRe.exec(line);
    if (m) {
      o = Number(m[1]);
      n = Number(m[2]);
      inHunk = true;
      out.push({ kind: "hunk", text: line });
    } else if (line.startsWith("diff --git")) {
      inHunk = false;
      out.push({ kind: "meta", text: line });
    } else if (!inHunk || line.startsWith("\\")) {
      out.push({ kind: "meta", text: line });
    } else if (line.startsWith("+")) {
      out.push({ kind: "add", text: line.slice(1), newNo: n++ });
    } else if (line.startsWith("-")) {
      out.push({ kind: "del", text: line.slice(1), oldNo: o++ });
    } else {
      out.push({ kind: "ctx", text: line.slice(1), oldNo: o++, newNo: n++ });
    }
  }
  return out;
}
