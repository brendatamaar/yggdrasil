import js from "@eslint/js";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
import { defineConfig, globalIgnores } from "eslint/config";
import globals from "globals";
import tseslint from "typescript-eslint";

export default defineConfig([
  // src/api/gen is written by tygo (task generate) and never edited by hand.
  globalIgnores(["dist", "node_modules", "src/api/gen"]),
  {
    files: ["**/*.{ts,tsx}"],
    extends: [
      js.configs.recommended,
      tseslint.configs.strictTypeChecked,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
    ],
    languageOptions: {
      ecmaVersion: 2022,
      globals: globals.browser,
      parserOptions: {
        projectService: true,
      },
    },
  },
  {
    // Widgets are self-contained modules and must not import each other
    // (docs/AGENTS.md 5.10). A widget folder may reach "../types" and anything
    // outside src/widgets. src/widgets/isolation.test.ts checks every depth.
    files: ["src/widgets/*/**/*.{ts,tsx}"],
    rules: {
      "no-restricted-imports": [
        "error",
        {
          patterns: [
            {
              regex: String.raw`^\.\./(?!types$)[^./][^/]*(/|$)`,
              message: "A widget can't import another widget. Share code through src/components or src/api.",
            },
          ],
        },
      ],
    },
  },
]);
