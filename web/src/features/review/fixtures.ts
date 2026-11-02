/** A fictional diff: one line-endings-only hunk and one real change. */
export const mixedDiff = [
  "diff --git a/src/a.ts b/src/a.ts",
  "--- a/src/a.ts",
  "+++ b/src/a.ts",
  "@@ -1,2 +1,2 @@",
  "-const a = 1;\r",
  "+const a = 1;",
  "@@ -10,2 +10,2 @@",
  " keep",
  "-old()",
  "+fresh()",
  "",
].join("\n");
