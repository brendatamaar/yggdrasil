const preferred = ["command", "file_path", "path", "pattern", "url", "query", "description", "prompt"];

/** One short line about a tool's input, for the collapsed view. */
export function summarizeInput(input: unknown, max = 80): string {
  let text = "";
  if (typeof input === "string") {
    text = input;
  } else if (typeof input === "object" && input !== null && !Array.isArray(input)) {
    const rec = input as Record<string, unknown>;
    const key = preferred.find((k) => typeof rec[k] === "string") ?? Object.keys(rec).find((k) => typeof rec[k] === "string");
    if (key) text = rec[key] as string;
  }
  text = text.replace(/\s+/g, " ").trim();
  return text.length > max ? `${text.slice(0, max - 1)}…` : text;
}

export function inputText(input: unknown): string {
  if (input === undefined || input === null) return "";
  return typeof input === "string" ? input : JSON.stringify(input, null, 2);
}

/** Cuts long text to a number of lines and characters; reports how many characters were left out. */
export function capText(text: string, maxLines = 60, maxChars = 6000): { shown: string; hidden: number } {
  let cut = text;
  const lines = cut.split("\n");
  if (lines.length > maxLines) cut = lines.slice(0, maxLines).join("\n");
  if (cut.length > maxChars) cut = cut.slice(0, maxChars);
  return { shown: cut, hidden: text.length - cut.length };
}

export type DiffLineKind = "add" | "del" | "hunk" | "meta" | "ctx";

/** Classifies unified-diff lines for colouring. Pure text in, text out. */
export function diffLines(diff: string): { kind: DiffLineKind; text: string }[] {
  return diff
    .replace(/\r\n/g, "\n")
    .replace(/\n$/, "")
    .split("\n")
    .map((text) => {
      let kind: DiffLineKind = "ctx";
      if (text.startsWith("+++") || text.startsWith("---") || text.startsWith("diff ") || text.startsWith("index ")) kind = "meta";
      else if (text.startsWith("@@")) kind = "hunk";
      else if (text.startsWith("+")) kind = "add";
      else if (text.startsWith("-")) kind = "del";
      return { kind, text };
    });
}
