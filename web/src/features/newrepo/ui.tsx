import type { ReactNode } from "react";

export const btn = "rounded border border-line px-3 py-1 text-sm hover:bg-surface disabled:opacity-50";
export const primary = "rounded bg-accent px-3 py-1 text-sm font-medium text-canvas hover:opacity-90 disabled:opacity-50";
export const input = "w-full rounded-md border border-line bg-canvas px-2 py-1 text-sm";

export function Footer({ children }: { children: ReactNode }) {
  return <div className="flex flex-wrap justify-end gap-2">{children}</div>;
}

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="flex flex-col gap-1 text-sm">
      <span className="font-medium">{label}</span>
      {children}
      {hint ? <span className="text-xs text-muted">{hint}</span> : null}
    </label>
  );
}

/** A list of plain-text lines the person confirms: exactly what the server previewed. */
export function Lines({ label, lines }: { label: string; lines: readonly string[] }) {
  return (
    <ul aria-label={label} className="list-disc space-y-1 pl-5 text-sm">
      {lines.map((l, i) => (
        <li key={i} className="font-mono text-xs break-words whitespace-pre-wrap">
          {l}
        </li>
      ))}
    </ul>
  );
}

export function Problems({ problems }: { problems: readonly string[] | undefined }) {
  if (!problems || problems.length === 0) return null;
  return (
    <div role="alert" className="space-y-1 rounded border border-danger p-2 text-sm text-danger">
      <p className="font-medium">Fix this before you continue</p>
      <ul className="list-disc pl-5">
        {problems.map((p, i) => (
          <li key={i}>{p}</li>
        ))}
      </ul>
    </div>
  );
}

export function Hash({ hash }: { hash: string }) {
  return (
    <p className="text-xs text-muted">
      Confirmation code: <code data-testid="spec-hash" className="font-mono break-all">{hash}</code>
    </p>
  );
}

/** An explicit empty state (success with nothing to show), distinct from pending and error. */
export function Guidance({ kind, children }: { kind: "NoRemote" | "NoCredentials"; children: ReactNode }) {
  return (
    <p data-empty={kind} className="rounded border border-line bg-surface p-2 text-sm text-muted">
      {children}
    </p>
  );
}
