import { safeHref } from "../../lib/safeHref";

const editorSchemes = new Set(["vscode:", "cursor:", "idea:"]);

/**
 * Returns the URL only when it may be opened as an editor link: vscode:,
 * cursor:, idea:, or a plain http(s) URL without credentials. Anything else
 * (javascript:, data:, ...) gives undefined.
 */
export function safeEditorHref(url: string | null | undefined): string | undefined {
  if (!url) return undefined;
  const trimmed = url.trim();
  let u: URL;
  try {
    u = new URL(trimmed);
  } catch {
    return undefined;
  }
  if (editorSchemes.has(u.protocol)) return trimmed;
  return safeHref(trimmed);
}
