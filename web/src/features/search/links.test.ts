import { describe, expect, it } from "vitest";
import { safeEditorHref } from "./links";

describe("safeEditorHref", () => {
  it.each(["vscode://file/c:/x/a.ts:3:1", "cursor://file/x:1:1", "idea://open?file=a&line=3", "https://git.acme.dev/a", "http://localhost/a"])(
    "allows %s",
    (u) => {
      expect(safeEditorHref(u)).toBe(u);
    },
  );
  it.each(["javascript:alert(1)", "data:text/html,<b>x</b>", "file:///etc/passwd", "ftp://x/y", "https://u:p@host/x", "not a url", ""])(
    "rejects %s",
    (u) => {
      expect(safeEditorHref(u)).toBeUndefined();
    },
  );
  it("rejects missing values", () => {
    expect(safeEditorHref(undefined)).toBeUndefined();
    expect(safeEditorHref(null)).toBeUndefined();
  });
});
