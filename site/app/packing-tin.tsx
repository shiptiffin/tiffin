import "./packing-tin.css";

/* The hero: what your apps need flies into a stacked tin, tier by tier, then the lid shuts
   and the tin smiles. Pure SVG and CSS, so it plays before any script loads. The packed tin
   is the default style; the motion only runs with prefers-reduced-motion: no-preference. */

const CX = 280; // the tin's centre line
const W = 124; // half a tier's width
const RY = 26; // the rim's ellipse
const L = 5; // how far the rolled rim stands out
const D = 84; // a tier's depth
const RIM = { base: 384, mid: 384 - D, top: 384 - 2 * D } as const;
type Tier = keyof typeof RIM;

/* Timeline, in ms. Each tier lands, then its three parts fly in; the lid goes on last. */
const TIER_AT: Record<Tier, number> = { base: 0, mid: 600, top: 1220 };
const FLY = 460; // one part's flight
const LID_AT = 1900;
const CLICK = LID_AT + 340; // the lid seats; everything after keys off this

type Part = { id: string; label: string; tier: Tier; x: number; y: number; land: number; at: number };
/* Where each part waits (x, y) and where it lands in its tier (land, across the centre line). */
const PARTS: Part[] = [
  { id: "signin", label: "Sign-in", tier: "base", x: 70, y: 420, land: -60, at: 180 },
  { id: "email", label: "Email", tier: "base", x: 492, y: 420, land: 60, at: 250 },
  { id: "analytics", label: "Analytics", tier: "base", x: 502, y: 296, land: 0, at: 320 },
  { id: "database", label: "Database", tier: "mid", x: 58, y: 296, land: -60, at: 860 },
  { id: "kv", label: "KV", tier: "mid", x: 486, y: 172, land: 60, at: 930 },
  { id: "files", label: "Files", tier: "mid", x: 74, y: 172, land: 0, at: 1000 },
  { id: "jobs", label: "Jobs", tier: "top", x: 160, y: 62, land: -60, at: 1480 },
  { id: "domains", label: "Domains", tier: "top", x: 400, y: 62, land: 60, at: 1550 },
  { id: "apps", label: "Your apps", tier: "top", x: 280, y: 34, land: 0, at: 1620 },
];

/* What each tier holds, on its enamel band. */
const BANDS: Record<Tier, { label: string; enamel: string }> = {
  top: { label: "APPS · JOBS · DOMAINS", enamel: "leaf" },
  mid: { label: "DATABASE · KV · FILES", enamel: "teal" },
  base: { label: "SIGN-IN · EMAIL · ANALYTICS", enamel: "plum" },
};

const ms = (n: number) => `${n}ms`;
const vars = (o: Record<string, string>) => o as React.CSSProperties;

/* Each icon is drawn around (0,0), about 56 across. */
function Icon({ id }: { id: string }) {
  switch (id) {
    case "apps":
      return (
        <g>
          <rect className="o paper" x="-34" y="-25" width="68" height="50" rx="5" />
          <path className="o lit" d="M-34-20a5 5 0 0 1 5-5h58a5 5 0 0 1 5 5v9h-68z" />
          <circle cx="-25" cy="-17.5" r="2.6" className="dot" />
          <circle cx="-17" cy="-17.5" r="2.6" className="dot" />
          <rect className="brass" x="-22" y="-2" width="20" height="16" rx="2" />
          <path className="o thin" d="M5 0h17M5 8h13M5 16h17" />
        </g>
      );
    case "database":
      return (
        <g>
          <path className="o body" d="M-24-20v40a24 8 0 0 0 48 0v-40" />
          <path className="o brass" d="M-24-2a24 8 0 0 0 48 0v10a24 8 0 0 1-48 0z" />
          <ellipse className="o lit" cx="0" cy="-20" rx="24" ry="8" />
        </g>
      );
    case "kv":
      return (
        <g>
          <rect className="o paper" x="-30" y="-22" width="60" height="44" rx="4" />
          <path className="o thin" d="M-30-7h60M-30 8h60M-6-22v44" />
          <rect className="brass" x="-24" y="-17" width="12" height="6" rx="1.5" />
          <rect className="brass" x="-24" y="-2" width="12" height="6" rx="1.5" />
          <rect className="brass" x="-24" y="13" width="12" height="6" rx="1.5" />
          <path className="o thin" d="M2-14h20M2 1h14M2 16h18" />
        </g>
      );
    case "files":
      return (
        <g>
          <rect className="o paper" x="-16" y="-34" width="36" height="46" transform="rotate(9 2 -11)" />
          <rect className="o paper" x="-20" y="-30" width="36" height="46" transform="rotate(-5 -2 -7)" />
          <path className="o brass" d="M-26-12h15l6-6h31v42h-52z" />
        </g>
      );
    case "signin":
      return (
        <g transform="rotate(-24)">
          <circle className="o brass" cx="16" cy="0" r="16" />
          <circle className="o hollow" cx="20" cy="-2" r="4.5" />
          <path className="o lit" d="M-34-5h34v10h-12v9h-8v-9h-5v7h-8v-7h-1z" />
        </g>
      );
    case "email":
      return (
        <g transform="rotate(-6)">
          <rect className="o paper" x="-30" y="-21" width="60" height="42" rx="3" />
          <path className="o thin" d="M-28-18l28 21 28-21" />
        </g>
      );
    case "jobs":
      return (
        <g>
          <circle className="o brass" r="27" />
          <circle className="o paper" r="20" />
          <path className="o thin" d="M0-11V0l8 6" />
        </g>
      );
    case "analytics":
      return (
        <g>
          <rect className="o lit" x="-27" y="2" width="15" height="22" />
          <rect className="o paper" x="-8" y="-10" width="15" height="34" />
          <rect className="o brass" x="11" y="-26" width="15" height="50" />
        </g>
      );
    case "domains":
      return (
        <g>
          <circle className="o paper" r="26" />
          <ellipse className="o thin" rx="11" ry="26" />
          <path className="o thin" d="M-26 0h52M-22-13h44M-22 13h44" />
          <path className="o brass" d="M8 6l18 7-8 3-3 8z" />
        </g>
      );
    default:
      return null;
  }
}

/* A tier is drawn in two halves, so the parts can sit inside it: the hollow behind them,
   the body and rim in front of them. */
function TierBack({ y }: { y: number }) {
  return <ellipse className="o hollow" cx={CX} cy={y} rx={W + L} ry={RY} />;
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
      <path id={`pk-arc-${t}`} className="none" d={`M${CX - W} ${y + 33}A${W} ${RY} 0 0 0 ${CX + W} ${y + 33}`} />
      <text className="band-label">
        <textPath href={`#pk-arc-${t}`} startOffset="50%" textAnchor="middle">
          {BANDS[t].label}
        </textPath>
      </text>
      <path className="o thin" d={band} />
      <path className="shine" d={`M${CX - W + 24} ${y + 48}v${D - 34}q7 5 15 8v${-(D - 34)}q-8-3-15-8z`} />
      <path className="seam" d={`M${CX - W} ${y + 11}A${W} ${RY} 0 0 0 ${CX + W} ${y + 11}`} />
      <path
        className="o lit"
        d={`M${CX - lip} ${y}v8A${lip} ${RY} 0 0 0 ${CX + lip} ${y + 8}v-8A${lip} ${RY} 0 0 1 ${CX - lip} ${y}Z`}
      />
    </>
  );
}

export function PackingTin() {
  const tiers: Tier[] = ["base", "mid", "top"];
  const lidTop = RIM.top - 24;
  return (
    <svg
      className="pack"
      viewBox="0 0 560 500"
      role="img"
      aria-labelledby="pack-title"
      style={vars({ "--click": ms(CLICK), "--fly": ms(FLY) })}
    >
      <title id="pack-title">
        Sign-in, email, analytics, a database, KV, files, jobs, domains and your apps, packed into one stacked tin
      </title>
      <defs>
        <pattern id="pk-speckle" width="23" height="19" patternUnits="userSpaceOnUse">
          <circle cx="3" cy="4" r="0.9" />
          <circle cx="14" cy="2" r="0.7" />
          <circle cx="9" cy="11" r="1" />
          <circle cx="19" cy="13" r="0.8" />
          <circle cx="4" cy="16" r="0.6" />
        </pattern>
      </defs>

      <ellipse className="floor" cx={CX} cy={RIM.base + D + 10} rx="158" ry="20" />

      <g className="settle">
        {tiers.map((t) => (
          <g key={t}>
            <g className={`pk-tier pk-tier-${t}`} style={vars({ "--at": ms(TIER_AT[t]) })}>
              <TierBack y={RIM[t]} />
            </g>
            {PARTS.filter((p) => p.tier === t).map((p) => (
              <g
                key={p.id}
                className="pk-part"
                style={vars({
                  "--at": ms(p.at),
                  "--dx": `${CX + p.land - p.x}px`,
                  "--dy": `${RIM[t] + 34 - p.y}px`,
                })}
              >
                <g className="part-x">
                  <g className="part-y">
                    <g transform={`translate(${p.x} ${p.y})`}>
                      <g className="part-spin">
                        <Icon id={p.id} />
                      </g>
                    </g>
                  </g>
                </g>
                <text className="part-label" x={p.x} y={p.y + 50} textAnchor="middle">
                  {p.label}
                </text>
              </g>
            ))}
            <g className={`pk-tier pk-tier-${t}`} style={vars({ "--at": ms(TIER_AT[t]) })}>
              <TierFront t={t} />
              {t === "mid" ? (
                <g>
                  <path className="o thin" d={`M${CX - W} ${RIM.mid + 46}q-12 0-18 10`} />
                  <circle className="o body" cx={CX - W - 24} cy={RIM.mid + 62} r="12" />
                  <path className="o thin" d={`M${CX + W} ${RIM.mid + 46}q12 0 18 10`} />
                  <circle className="o body" cx={CX + W + 24} cy={RIM.mid + 62} r="12" />
                </g>
              ) : null}
            </g>
          </g>
        ))}

        <g className="lid" style={vars({ "--at": ms(LID_AT) })}>
          <path
            className="o body"
            d={`M${CX - W - 9} ${lidTop}V${RIM.top + 2}A${W + 9} ${RY + 3} 0 0 0 ${CX + W + 9} ${RIM.top + 2}V${lidTop}Z`}
          />
          <path
            className="speckle"
            d={`M${CX - W - 9} ${lidTop}V${RIM.top + 2}A${W + 9} ${RY + 3} 0 0 0 ${CX + W + 9} ${RIM.top + 2}V${lidTop}Z`}
          />
          <ellipse className="o lit" cx={CX} cy={lidTop} rx={W + 9} ry={RY + 3} />
          <ellipse className="o body" cx={CX} cy={lidTop - 5} rx={W - 30} ry={RY - 9} />
          <ellipse className="lid-top" cx={CX} cy={lidTop - 8} rx={W - 34} ry={RY - 13} />
          <g className="handle">
            <path className="handle-bar" d={`M${CX - 44} ${lidTop - 8}v-34q0-20 20-20h48q20 0 20 20v34`} />
            <path className="handle-shine" d={`M${CX - 44} ${lidTop - 8}v-34q0-20 20-20h48q20 0 20 20v34`} />
          </g>
          <rect className="o body" x={CX - 53} y={lidTop - 18} width="18" height="17" rx="4" />
          <rect className="o body" x={CX + 35} y={lidTop - 18} width="18" height="17" rx="4" />
        </g>

        {/* The click: three short strokes at each side of the lid, there and gone. */}
        <g className="ticks">
          <path d={`M${CX - W - 22} ${lidTop + 2}l-14-6M${CX - W - 22} ${lidTop + 14}h-16M${CX - W - 20} ${lidTop + 26}l-13 7`} />
          <path d={`M${CX + W + 22} ${lidTop + 2}l14-6M${CX + W + 22} ${lidTop + 14}h16M${CX + W + 20} ${lidTop + 26}l13 7`} />
        </g>

        <g className="face">
          <g className="eyes">
            <circle cx={CX - 26} cy={RIM.mid + 70} r="6.5" className="dot" />
            <circle cx={CX + 26} cy={RIM.mid + 70} r="6.5" className="dot" />
          </g>
          <path className="o thin mouth" d={`M${CX - 11} ${RIM.mid + 86}q11 9 22 0`} />
        </g>
      </g>
    </svg>
  );
}
