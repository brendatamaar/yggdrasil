/**
 * Returns the URL only when it is an absolute http(s) URL, else undefined.
 * Host-provided URLs must pass through this before reaching an href.
 */
export function safeHref(url: string | null | undefined): string | undefined {
  if (!url) return undefined;
  let u: URL;
  try {
    u = new URL(url);
  } catch {
    return undefined;
  }
  return u.protocol === "http:" || u.protocol === "https:" ? url.trim() : undefined;
}
