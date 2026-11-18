import { EMPTY_TEXT, type EmptyKind } from "./empty";

export function EmptyNote({ kind }: { kind: EmptyKind }) {
  return (
    <p data-empty={kind} className="text-sm text-muted">
      {EMPTY_TEXT[kind]}
    </p>
  );
}
