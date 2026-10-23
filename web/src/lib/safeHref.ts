/**
 * Returns the URL only when it is an absolute http(s) URL, else undefined.
 * URLs carrying credentials (userinfo) are rejected.
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
  if (u.protocol !== "http:" && u.protocol !== "https:") return undefined;
  if (u.username !== "" || u.password !== "") return undefined;
  return url.trim();
}
