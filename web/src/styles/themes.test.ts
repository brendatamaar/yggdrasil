import { describe, expect, it } from "vitest";
const themesCss = Object.values(import.meta.glob<string>("./themes.css", { query: "?raw", import: "default", eager: true }))[0] ?? "";
import { THEMES } from "../stores/theme";

type Tokens = Record<string, string>;

function parseThemes(css: string): Map<string, Tokens> {
  const out = new Map<string, Tokens>();
  for (const m of css.matchAll(/([^{}]+)\{([^}]*)\}/g)) {
    const selector = m[1] ?? "";
    const tokens: Tokens = {};
    for (const d of (m[2] ?? "").matchAll(/--([a-z]+):\s*(#[0-9a-fA-F]{6})\s*;/g)) {
      tokens[d[1] ?? ""] = d[2] ?? "";
    }
    for (const s of selector.matchAll(/\[data-theme="([^"]+)"\]/g)) {
      out.set(s[1] ?? "", { ...(out.get(s[1] ?? "") ?? {}), ...tokens });
    }
  }
  return out;
}

function luminance(hex: string): number {
  const channel = (i: number) => {
    const c = parseInt(hex.slice(1 + i * 2, 3 + i * 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(0) + 0.7152 * channel(1) + 0.0722 * channel(2);
}

function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x) as [number, number];
  return (hi + 0.05) / (lo + 0.05);
}

const pairs: readonly [string, string][] = [
  ["fg", "canvas"],
  ["fg", "surface"],
  ["muted", "canvas"],
  ["muted", "surface"],
  ["accent", "canvas"],
  ["canvas", "accent"], // button label on the accent fill
  ["ok", "canvas"],
  ["warn", "canvas"],
  ["danger", "canvas"],
];

describe("theme contrast (WCAG AA, 4.5:1)", () => {
  const themes = parseThemes(themesCss);

  it("defines tokens for every concrete theme", () => {
    for (const t of THEMES) {
      if (t !== "system") expect(themes.has(t), t).toBe(true);
    }
  });

  for (const t of THEMES) {
    if (t === "system") continue;
    it(`${t} meets AA for its main pairs`, () => {
      const tokens = themes.get(t) ?? {};
      for (const [fg, bg] of pairs) {
        const a = tokens[fg];
        const b = tokens[bg];
        expect(a, `${t} --${fg}`).toBeDefined();
        expect(b, `${t} --${bg}`).toBeDefined();
        expect(contrast(a ?? "", b ?? ""), `${t}: ${fg} on ${bg}`).toBeGreaterThanOrEqual(4.5);
      }
    });
  }
});
