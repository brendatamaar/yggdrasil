import type { ReactNode } from "react";
import { safeHref } from "../lib/safeHref";

/** Anchor for host-provided URLs. Unsafe schemes render plain text, never an anchor. */
export function SafeLink({ href, className, children }: { href: string | null | undefined; className?: string; children: ReactNode }) {
  const safe = safeHref(href);
  if (!safe) return <span className={className}>{children}</span>;
  return (
    <a className={className} href={safe} target="_blank" rel="noreferrer noopener">
      {children}
    </a>
  );
}
