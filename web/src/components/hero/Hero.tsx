import { useEffect, useState, type ReactNode } from "react";
import { HERO_MODE_LABELS, HERO_MODES, isHeroMode, resolveHeroPhase, useHeroStore } from "../../stores/hero";
import { HeroScene } from "./HeroScene";
import { SUGGESTIONS } from "./suggestions";
import { usePrefersReducedMotion } from "./usePrefersReducedMotion";

/** Re-reads the clock once a minute so auto mode crosses phase boundaries. */
function useNow(enabled: boolean): Date {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    if (!enabled) return;
    const timer = setInterval(() => {
      setNow(new Date());
    }, 60_000);
    return () => {
      clearInterval(timer);
    };
  }, [enabled]);
  return now;
}

interface HeroProps {
  /** The task composer, drawn on top of the scene. */
  children: ReactNode;
  /** Called with the suggestion text. The caller fills the composer; nothing is submitted. */
  onSuggest: (text: string) => void;
  /** Overrides the clock (tests). */
  now?: Date;
}

export function Hero({ children, onSuggest, now: nowOverride }: HeroProps) {
  const mode = useHeroStore((s) => s.mode);
  const setMode = useHeroStore((s) => s.setMode);
  const clock = useNow(mode === "auto" && nowOverride === undefined);
  const reduced = usePrefersReducedMotion();
  const phase = resolveHeroPhase(mode, nowOverride ?? clock);

  return (
    <section aria-label="Start a task" className="relative overflow-hidden rounded-xl border border-line">
      {phase ? <HeroScene phase={phase} animated={!reduced} /> : <div className="absolute inset-0 bg-surface" />}
      <div className="relative space-y-4 px-4 py-8 sm:px-8">
        <div className="mx-auto max-w-3xl rounded-lg border border-line bg-canvas p-4 shadow-lg">{children}</div>
        <ul aria-label="Suggestions" className="mx-auto flex max-w-3xl flex-wrap justify-center gap-2">
          {SUGGESTIONS.map((text) => (
            <li key={text}>
              <button
                type="button"
                onClick={() => {
                  onSuggest(text);
                }}
                className="rounded-full border border-line bg-canvas px-3 py-1 text-sm text-fg hover:bg-surface"
              >
                {text}
              </button>
            </li>
          ))}
        </ul>
        <div className="flex justify-end">
          <label className="flex items-center gap-2 rounded bg-canvas px-2 py-1 text-xs text-muted">
            Scene
            <select
              value={mode}
              onChange={(e) => {
                if (isHeroMode(e.target.value)) setMode(e.target.value);
              }}
              className="rounded border border-line bg-canvas px-1 py-0.5 text-fg"
            >
              {HERO_MODES.map((m) => (
                <option key={m} value={m}>
                  {HERO_MODE_LABELS[m]}
                </option>
              ))}
            </select>
          </label>
        </div>
      </div>
    </section>
  );
}
