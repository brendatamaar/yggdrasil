import { useRef, type KeyboardEvent, type ReactNode } from "react";

/** Arrow Up/Down (and Home/End) move focus between the `data-nav` buttons inside. */
export function NavList({ label, children, className }: { label: string; children: ReactNode; className?: string }) {
  const ref = useRef<HTMLUListElement>(null);
  const onKeyDown = (e: KeyboardEvent) => {
    const items = Array.from(ref.current?.querySelectorAll<HTMLElement>("[data-nav]") ?? []);
    const i = items.indexOf(document.activeElement as HTMLElement);
    if (i < 0) return;
    let next = -1;
    if (e.key === "ArrowDown") next = Math.min(items.length - 1, i + 1);
    else if (e.key === "ArrowUp") next = Math.max(0, i - 1);
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = items.length - 1;
    if (next < 0) return;
    e.preventDefault();
    items[next]?.focus();
  };
  return (
    <ul ref={ref} aria-label={label} onKeyDown={onKeyDown} className={className}>
      {children}
    </ul>
  );
}

const toneClass = { muted: "text-muted", warn: "text-warn", danger: "text-danger" };

export function Notice({ children, tone = "muted" }: { children: ReactNode; tone?: "muted" | "warn" | "danger" }) {
  return (
    <p role={tone === "danger" ? "alert" : undefined} className={`text-sm ${toneClass[tone]}`}>
      {children}
    </p>
  );
}

export interface TabDef {
  id: string;
  label: string;
}

/** An accessible tab strip: Left/Right/Home/End move and select. The panel is rendered by the caller. */
export function Tabs({ tabs, value, onChange, label }: { tabs: TabDef[]; value: string; onChange: (id: string) => void; label: string }) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const onKeyDown = (e: KeyboardEvent) => {
    const i = tabs.findIndex((t) => t.id === value);
    let next = -1;
    if (e.key === "ArrowRight") next = (i + 1) % tabs.length;
    else if (e.key === "ArrowLeft") next = (i - 1 + tabs.length) % tabs.length;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = tabs.length - 1;
    const t = tabs[next];
    if (!t) return;
    e.preventDefault();
    onChange(t.id);
    refs.current[next]?.focus();
  };
  return (
    <div role="tablist" aria-label={label} onKeyDown={onKeyDown} className="flex gap-1 border-b border-line">
      {tabs.map((t, i) => (
        <button
          key={t.id}
          ref={(el) => {
            refs.current[i] = el;
          }}
          role="tab"
          id={`tab-${t.id}`}
          aria-selected={t.id === value}
          aria-controls={`panel-${t.id}`}
          tabIndex={t.id === value ? 0 : -1}
          onClick={() => {
            onChange(t.id);
          }}
          className={`-mb-px border-b-2 px-3 py-1 text-sm ${t.id === value ? "border-accent text-fg" : "border-transparent text-muted hover:text-fg"}`}
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}
