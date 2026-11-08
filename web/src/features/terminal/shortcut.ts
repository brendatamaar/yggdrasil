/** Ctrl+` (Backquote). Without Meta or Alt, so it never clashes with a browser or OS shortcut. */
export function isToggleKey(e: Pick<KeyboardEvent, "ctrlKey" | "metaKey" | "altKey" | "key" | "code">): boolean {
  return e.ctrlKey && !e.metaKey && !e.altKey && (e.key === "`" || e.code === "Backquote");
}

/**
 * True when the key should toggle the dock. Typing in a text field keeps the key, except inside the
 * terminal itself (xterm uses a hidden textarea) where the shortcut is how you leave it.
 */
export function shouldToggle(e: KeyboardEvent): boolean {
  if (!isToggleKey(e)) return false;
  const t = e.target;
  if (!(t instanceof HTMLElement)) return true;
  if (t.closest(".xterm")) return true;
  if (t.isContentEditable) return false;
  return !(t instanceof HTMLInputElement || t instanceof HTMLTextAreaElement || t instanceof HTMLSelectElement);
}
