import { describe, expect, it } from "vitest";
import { safeHref } from "./safeHref";

describe("safeHref", () => {
  it.each([
    "javascript:alert(1)",
    "JaVaScRiPt:alert(1)",
    "  javascript:alert(1)",
    "\tjava\nscript:alert(1)",
    "data:text/html,<script>alert(1)</script>",
    "vbscript:msgbox(1)",
    "/relative/path",
    "//evil.example/x",
    "",
    "   ",
  ])("rejects %j", (u) => {
    expect(safeHref(u)).toBeUndefined();
  });

  it("rejects missing values", () => {
    expect(safeHref(undefined)).toBeUndefined();
    expect(safeHref(null)).toBeUndefined();
  });

  it.each(["https://git.acme.dev/x", "http://git.acme.dev/x", "HTTPS://git.acme.dev/x"])("accepts %s", (u) => {
    expect(safeHref(u)).toBe(u);
  });

  it("trims leading whitespace on valid URLs", () => {
    expect(safeHref("  https://git.acme.dev/x")).toBe("https://git.acme.dev/x");
  });
});
