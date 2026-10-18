import type { HeroPhase } from "../../stores/hero";

interface Sky {
  top: string;
  bottom: string;
  hill: string;
  hillFar: string;
  /** Sun or moon centre and colour. */
  body: { x: number; y: number; r: number; fill: string };
  stars: boolean;
  clouds: string | null;
}

const SKIES: Record<HeroPhase, Sky> = {
  dawn: {
    top: "#5b6fb5",
    bottom: "#f6b99a",
    hill: "#3b3f63",
    hillFar: "#7a6f9b",
    body: { x: 220, y: 150, r: 26, fill: "#ffe3b0" },
    stars: false,
    clouds: "#ffd9c4",
  },
  day: {
    top: "#4aa3e8",
    bottom: "#bfe6ff",
    hill: "#2f7d57",
    hillFar: "#5fae86",
    body: { x: 640, y: 60, r: 28, fill: "#fff3b0" },
    stars: false,
    clouds: "#ffffff",
  },
  dusk: {
    top: "#3b2f6b",
    bottom: "#f08a5d",
    hill: "#2a2146",
    hillFar: "#6b4a7a",
    body: { x: 560, y: 140, r: 30, fill: "#ffb36b" },
    stars: false,
    clouds: "#f4a98a",
  },
  night: {
    top: "#050a18",
    bottom: "#1b2a4e",
    hill: "#0a1020",
    hillFar: "#16213d",
    body: { x: 600, y: 60, r: 20, fill: "#e8eefc" },
    stars: true,
    clouds: null,
  },
};

// Fixed positions so the scene is stable between renders.
const STARS: readonly (readonly [number, number])[] = [
  [60, 30], [130, 80], [200, 20], [260, 60], [330, 35], [410, 90], [470, 25],
  [530, 70], [700, 40], [740, 100], [90, 120], [370, 130],
];

/** Decorative procedural scene: SVG only, no assets, no network. */
export function HeroScene({ phase, animated }: { phase: HeroPhase; animated: boolean }) {
  const sky = SKIES[phase];
  const id = `hero-sky-${phase}`;
  return (
    <svg
      aria-hidden="true"
      focusable="false"
      viewBox="0 0 800 240"
      preserveAspectRatio="xMidYMid slice"
      data-testid="hero-scene"
      data-phase={phase}
      data-animated={animated ? "true" : "false"}
      className={`absolute inset-0 h-full w-full ${animated ? "hero-animated" : ""}`}
    >
      <defs>
        <linearGradient id={id} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor={sky.top} />
          <stop offset="1" stopColor={sky.bottom} />
        </linearGradient>
      </defs>
      <rect width="800" height="240" fill={`url(#${id})`} />
      {sky.stars
        ? STARS.map(([x, y], i) => (
            <circle
              key={i}
              className="hero-star"
              cx={x}
              cy={y}
              r={1.4}
              fill="#ffffff"
              style={animated ? { animationDelay: `${String(i * 0.3)}s` } : undefined}
            />
          ))
        : null}
      <circle cx={sky.body.x} cy={sky.body.y} r={sky.body.r} fill={sky.body.fill} />
      {sky.clouds ? (
        <g className="hero-cloud" fill={sky.clouds} opacity="0.8">
          <ellipse cx="150" cy="70" rx="46" ry="12" />
          <ellipse cx="180" cy="62" rx="30" ry="10" />
          <ellipse cx="460" cy="40" rx="40" ry="10" />
          <ellipse cx="486" cy="33" rx="26" ry="9" />
        </g>
      ) : null}
      <path d="M0 190 Q120 130 260 175 T520 160 T800 180 V240 H0 Z" fill={sky.hillFar} />
      <path d="M0 215 Q160 165 330 205 T640 195 T800 210 V240 H0 Z" fill={sky.hill} />
    </svg>
  );
}
