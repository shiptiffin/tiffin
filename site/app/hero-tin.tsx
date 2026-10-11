import type { CSSProperties, ReactNode } from "react";
import { Icon } from "./tin-icons";
import "./hero-tin.css";

/* The hero's tin, full size: everything an app needs flies in from every edge of the screen
   in three beats, the tiers stack, the lid slams on and the tin smiles.

   The tin is drawn as stacked SVG layers in one box (back of a tier, its parts, front of the
   tier, the next tier...), so a part can fly in as an HTML element from far outside the tin
   and still drop *inside* it. Everything moves with transform and opacity only, from CSS, so
   it plays before any script loads. The packed tin is the default style; the motion only
   runs under prefers-reduced-motion: no-preference (hero-tin.css). */

/* Geometry, in the tin box's own units (viewBox 0 0 320 384). */
const VW = 320;
const VH = 384;
const CX = 160;
const W = 124; // half a tier's width
const RY = 26; // the rim's ellipse
const L = 5; // how far the rolled rim stands out
const D = 84; // a tier's depth
const RIM = { top: 96, mid: 180, base: 264 } as const;
type Tier = keyof typeof RIM;
const LID_TOP = RIM.top - 24;

/* The timeline (ms). Three beats of three parts, then the lid. hero-tin.css keys off --click. */
const HIT: Record<Tier, number> = { base: 700, mid: 900, top: 1100 };
const TIER_LAND: Record<Tier, number> = { base: 0, mid: 800, top: 1000 };
const CLICK = 1250;

type Part = {
  id: string;
  label: string;
  tier: Tier;
  /** Where it lands, across the tin's centre line (tin units). */
  land: number;
  /** Where it starts, from its landing spot: off screen. */
  from: [string, string];
  /** Where it peeks in at the edge before the rush. */
  peek: [string, string];
  tilt: number;
};

const PARTS: Part[] = [
  { id: "signin", label: "Sign-in", tier: "base", land: -60, from: ["-62vw", "48svh"], peek: ["-44vw", "34svh"], tilt: -24 },
  { id: "email", label: "Email", tier: "base", land: 60, from: ["62vw", "48svh"], peek: ["44vw", "34svh"], tilt: 20 },
  { id: "analytics", label: "Analytics", tier: "base", land: 0, from: ["68vw", "-4svh"], peek: ["50vw", "-4svh"], tilt: 14 },
  { id: "database", label: "Database", tier: "mid", land: -60, from: ["-68vw", "-6svh"], peek: ["-50vw", "-6svh"], tilt: -16 },
  { id: "kv", label: "KV", tier: "mid", land: 60, from: ["68vw", "-34svh"], peek: ["50vw", "-30svh"], tilt: 18 },
  { id: "files", label: "Files", tier: "mid", land: 0, from: ["-66vw", "-38svh"], peek: ["-48vw", "-34svh"], tilt: -12 },
  { id: "jobs", label: "Jobs", tier: "top", land: -60, from: ["-56vw", "-86svh"], peek: ["-42vw", "-64svh"], tilt: -20 },
  { id: "domains", label: "Domains", tier: "top", land: 60, from: ["56vw", "-86svh"], peek: ["42vw", "-64svh"], tilt: 22 },
  { id: "apps", label: "Your apps", tier: "top", land: 0, from: ["0vw", "-96svh"], peek: ["0vw", "-74svh"], tilt: 8 },
];

const BANDS: Record<Tier, { label: string; enamel: string }> = {
  top: { label: "APPS · JOBS · DOMAINS", enamel: "leaf" },
  mid: { label: "DATABASE · KV · FILES", enamel: "teal" },
  base: { label: "SIGN-IN · EMAIL · ANALYTICS", enamel: "plum" },
};

/* Act two (scrolling): each tier becomes a project with its own limit. Illustrative projects. */
const PROJECTS: Record<Tier, { name: string; limit: string; fill: number; held?: boolean }> = {
  top: { name: "shop", limit: "Up to 40% of the box", fill: 1, held: true },
  mid: { name: "guestbook", limit: "512 MB of memory", fill: 0.45 },
  base: { name: "reports", limit: "Up to 10% of the box", fill: 0.3 },
};

const ms = (n: number) => `${n}ms`;
const vars = (o: Record<string, string | number>) => o as CSSProperties;
const pct = (n: number, of: number) => `${((n / of) * 100).toFixed(3)}%`;

/** One full-size layer of the tin. */
function Layer({ className, style, children }: { className: string; style?: CSSProperties; children: ReactNode }) {
  return (
    <svg className={`ht-layer ${className}`} style={style} viewBox={`0 0 ${VW} ${VH}`} aria-hidden="true">
      {children}
    </svg>
  );
}

function TierBack({ t }: { t: Tier }) {
  return <ellipse className="o hollow" cx={CX} cy={RIM[t]} rx={W + L} ry={RY} />;
}

function TierFront({ t }: { t: Tier }) {
  const y = RIM[t];
  const lip = W + L;
  const body = `M${CX - W} ${y + 8}V${y + D}A${W} ${RY} 0 0 0 ${CX + W} ${y + D}V${y + 8}Z`;
  const band = `M${CX - W} ${y + 18}A${W} ${RY} 0 0 0 ${CX + W} ${y + 18}V${y + 40}A${W} ${RY} 0 0 1 ${CX - W} ${y + 40}Z`;
  return (
    <>
      <path className="o body" d={body} />
      <path className="speckle" d={body} />
      <path className="band" d={band} style={vars({ "--enamel": `var(--enamel-${BANDS[t].enamel})` })} />
      <path id={`ht-arc-${t}`} fill="none" d={`M${CX - W} ${y + 33}A${W} ${RY} 0 0 0 ${CX + W} ${y + 33}`} />
      <text className="band-label">
        <textPath href={`#ht-arc-${t}`} startOffset="50%" textAnchor="middle">
          {BANDS[t].label}
        </textPath>
      </text>
      <text className="band-label band-project">
        <textPath href={`#ht-arc-${t}`} startOffset="50%" textAnchor="middle">
          {PROJECTS[t].name.toUpperCase()}
        </textPath>
      </text>
      <path className="o thin" d={band} />
      <path className="shine" d={`M${CX - W + 24} ${y + 48}v${D - 34}q7 5 15 8v${-(D - 34)}q-8-3-15-8z`} />
      <path className="seam" d={`M${CX - W} ${y + 11}A${W} ${RY} 0 0 0 ${CX + W} ${y + 11}`} />
      <path
        className="o lit"
        d={`M${CX - lip} ${y}v8A${lip} ${RY} 0 0 0 ${CX + lip} ${y + 8}v-8A${lip} ${RY} 0 0 1 ${CX - lip} ${y}Z`}
      />
      {t === "mid" ? (
        <>
          <path className="o thin" d={`M${CX - W} ${y + 46}q-12 0-18 10`} />
          <circle className="o body" cx={CX - W - 24} cy={y + 62} r="12" />
          <path className="o thin" d={`M${CX + W} ${y + 46}q12 0 18 10`} />
          <circle className="o body" cx={CX + W + 24} cy={y + 62} r="12" />
        </>
      ) : null}
    </>
  );
}

function PartEl({ p }: { p: Part }) {
  const hit = HIT[p.tier];
  return (
    <div
      className={`ht-part ht-part-${p.land < 0 ? "l" : p.land > 0 ? "r" : "c"}`}
      style={vars({
        left: pct(CX + p.land, VW),
        top: pct(RIM[p.tier] + 30, VH),
        "--fx": p.from[0],
        "--fy": p.from[1],
        "--px": p.peek[0],
        "--py": p.peek[1],
        "--tilt": `${p.tilt}deg`,
        "--hit": ms(hit),
      })}
    >
      <div className="ht-lift">
        <div className="ht-peek">
          <div className="ht-fly">
            <svg className="ht-icon" viewBox="-40 -40 80 80" aria-hidden="true">
              <Icon id={p.id} />
            </svg>
            <span className="ht-label">{p.label}</span>
          </div>
        </div>
        <span className="ht-tag-label">{p.label}</span>
      </div>
    </div>
  );
}

function Lid() {
  const r = W + 9;
  const band = `M${CX - r} ${LID_TOP}V${RIM.top + 2}A${r} ${RY + 3} 0 0 0 ${CX + r} ${RIM.top + 2}V${LID_TOP}Z`;
  const bar = `M${CX - 44} ${LID_TOP - 8}v-34q0-20 20-20h48q20 0 20 20v34`;
  return (
    <>
      <path className="o body" d={band} />
      <path className="speckle" d={band} />
      <ellipse className="o lit" cx={CX} cy={LID_TOP} rx={r} ry={RY + 3} />
      <ellipse className="o body" cx={CX} cy={LID_TOP - 5} rx={W - 30} ry={RY - 9} />
      <ellipse className="lid-top" cx={CX} cy={LID_TOP - 8} rx={W - 34} ry={RY - 13} />
      <g className="ht-handle">
        <path className="handle-bar" d={bar} />
        <path className="handle-shine" d={bar} />
      </g>
      <rect className="o body" x={CX - 53} y={LID_TOP - 18} width="18" height="17" rx="4" />
      <rect className="o body" x={CX + 35} y={LID_TOP - 18} width="18" height="17" rx="4" />
    </>
  );
}

export function HeroTin() {
  const tiers: Tier[] = ["base", "mid", "top"];
  return (
    <div
      className="ht"
      role="img"
      aria-label="Sign-in, email, analytics, a database, KV, files, jobs, domains and your apps fly in from every side and pack into one stacked tin, which shuts and smiles."
      style={vars({ "--click": ms(CLICK), "--aspect": `${VW} / ${VH}` })}
    >
      <svg width="0" height="0" aria-hidden="true" className="ht-defs">
        <defs>
          <pattern id="ht-speckle" width="23" height="19" patternUnits="userSpaceOnUse">
            <circle cx="3" cy="4" r="0.9" />
            <circle cx="14" cy="2" r="0.7" />
            <circle cx="9" cy="11" r="1" />
            <circle cx="19" cy="13" r="0.8" />
            <circle cx="4" cy="16" r="0.6" />
          </pattern>
        </defs>
      </svg>

      {/* The click: gold lines thrown out from the lid, there and gone. */}
      <svg className="ht-burst" viewBox="-200 -200 400 400" aria-hidden="true">
        {Array.from({ length: 8 }, (_, i) => {
          const a = (i / 8) * Math.PI * 2 + Math.PI / 8;
          const [x1, y1, x2, y2] = [Math.cos(a) * 150, Math.sin(a) * 70, Math.cos(a) * 190, Math.sin(a) * 92];
          return <line key={i} x1={x1.toFixed(1)} y1={y1.toFixed(1)} x2={x2.toFixed(1)} y2={y2.toFixed(1)} />;
        })}
      </svg>

      <div className="ht-tin">
        <Layer className="ht-floor">
          <ellipse className="floor" cx={CX} cy={RIM.base + D + 12} rx="150" ry="18" />
        </Layer>
        <div className="ht-squash">
          {tiers.map((t) => (
            <div key={t} className={`ht-tierset ht-set-${t}`}>
              <Layer className={`ht-tier ht-tier-${t}`} style={vars({ "--land": ms(TIER_LAND[t]) })}>
                <TierBack t={t} />
              </Layer>
              {PARTS.filter((p) => p.tier === t).map((p) => (
                <PartEl key={p.id} p={p} />
              ))}
              <Layer className={`ht-tier ht-tier-${t}`} style={vars({ "--land": ms(TIER_LAND[t]) })}>
                <TierFront t={t} />
              </Layer>
              <div
                className={`ht-gauge${PROJECTS[t].held ? " ht-held" : ""}`}
                style={vars({ top: pct(RIM[t] + 46, VH), "--fill": PROJECTS[t].fill })}
                aria-hidden="true"
              >
                <span className="ht-gauge-limit">
                  <span className="ht-gauge-name">{PROJECTS[t].name}</span> {PROJECTS[t].limit}
                </span>
                <span className="ht-gauge-bar">
                  <span />
                </span>
                {PROJECTS[t].held ? <span className="ht-gauge-held">Held at its limit</span> : null}
              </div>
            </div>
          ))}
          <div className="ht-lidset">
            <Layer className="ht-lid">
              <Lid />
            </Layer>
            <div className="ht-price" style={vars({ top: pct(LID_TOP - 62, VH) })} aria-hidden="true">
              <span className="ht-price-string" />
              <span className="ht-price-tag">
                <strong>$19</strong> a month
              </span>
            </div>
          </div>
          <div className="ht-faceset">
            <Layer className="ht-face">
              <g className="ht-eyes">
                <circle cx={CX - 26} cy={RIM.mid + 70} r="6.5" className="dot" />
                <circle cx={CX + 26} cy={RIM.mid + 70} r="6.5" className="dot" />
              </g>
              <path className="o thin ht-mouth" pathLength="1" d={`M${CX - 11} ${RIM.mid + 86}q11 9 22 0`} />
            </Layer>
          </div>
        </div>
      </div>
    </div>
  );
}
