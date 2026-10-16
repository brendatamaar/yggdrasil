import { describe, expect, it } from "vitest";

// Widgets are self-contained (docs/AGENTS.md 5.10): a file in
// src/widgets/<name>/ may import src/widgets/types and anything outside
// src/widgets, but not another widget folder and not the registry.

/** Relative imports of one source file. */
function specifiers(source: string): string[] {
  const out: string[] = [];
  for (const m of source.matchAll(/(?:from\s+|import\s*\(\s*|import\s+)["'](\.[^"']*)["']/g)) {
    if (m[1]) out.push(m[1]);
  }
  return out;
}

/**
 * Resolves `spec` against the folder of `file` (both relative to
 * src/widgets). Returns the path segments inside src/widgets, or null when the
 * import leaves src/widgets.
 */
function resolveInWidgets(file: string, spec: string): string[] | null {
  const stack = file.replace(/^\.\//, "").split("/").slice(0, -1);
  for (const seg of spec.split("/")) {
    if (seg === "." || seg === "") continue;
    if (seg === "..") {
      if (stack.length === 0) return null;
      stack.pop();
    } else {
      stack.push(seg);
    }
  }
  return stack;
}

/** Files are keyed like "./working-now/WorkingNow.tsx". Returns the broken rules. */
function crossWidgetImports(files: Record<string, string>): string[] {
  const bad: string[] = [];
  for (const [file, source] of Object.entries(files)) {
    const own = file.replace(/^\.\//, "").split("/");
    if (own.length < 2) continue; // types.ts and registry.ts are the shared files
    for (const spec of specifiers(source)) {
      const target = resolveInWidgets(file, spec);
      if (!target || target.length === 0) continue; // outside src/widgets
      const first = target[0];
      if (first !== own[0] && first !== "types") bad.push(`${file} imports ${spec}`);
    }
  }
  return bad;
}

describe("widget isolation", () => {
  it("no widget imports another widget or the registry", () => {
    const sources = import.meta.glob(["./**/*.{ts,tsx}", "!./**/*.test.{ts,tsx}"], {
      query: "?raw",
      import: "default",
      eager: true,
    });
    expect(Object.keys(sources).length).toBeGreaterThan(2);
    expect(crossWidgetImports(sources)).toEqual([]);
  });

  it("the check itself catches a sibling import, at any depth", () => {
    const bad = crossWidgetImports({
      "./a/A.tsx": `import { B } from "../b/B";`,
      "./a/deep/D.tsx": `import x from "../../b/B"; const m = await import("../../c/C");`,
      "./a/R.tsx": `import { widgetRegistry } from "../registry";`,
    });
    expect(bad).toEqual([
      `./a/A.tsx imports ../b/B`,
      `./a/deep/D.tsx imports ../../b/B`,
      `./a/deep/D.tsx imports ../../c/C`,
      `./a/R.tsx imports ../registry`,
    ]);
  });

  it("the check allows the shared types, its own folder and the rest of the app", () => {
    expect(
      crossWidgetImports({
        "./a/A.tsx": `import type { WidgetModule } from "../types"; import { Y } from "./Y"; import { q } from "../../api/queries";`,
        "./a/deep/D.tsx": `import { Y } from "../Y"; import type { WidgetModule } from "../../types"; import z from "../../../stores/connection";`,
        "./registry.ts": `import { a } from "./a";`,
      }),
    ).toEqual([]);
  });
});
