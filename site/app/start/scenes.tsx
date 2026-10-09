// Little animated scenes for the progress screen, drawn like the mark: the
// steel tin with its face, ink outlines, brass for the one thing that
// matters. CSS animations live in cloud.css (.sc-*); with reduced motion
// every scene rests on its last, meaningful frame.
import type { StageId } from "@/lib/cloud/progress";

export type SceneId = StageId | "waiting" | "ready" | "stopped";

type Face = "smile" | "grin" | "flat";

/** The mark's tin on its 32-unit grid, placed with its feet at (cx, ground). */
function Tin({
  cx,
  ground,
  s,
  face = "smile",
  wave,
  cheer,
  bob,
}: {
  cx: number;
  ground: number;
  s: number;
  face?: Face;
  wave?: boolean;
  cheer?: boolean;
  bob?: boolean;
}) {
  const handY = cheer ? 10.5 : 20.5;
  const reach = cheer ? 1.2 : 0;
  return (
    <g className={bob ? "sc-bob" : undefined}>
      <g transform={`translate(${cx - 16 * s} ${ground - 28.6 * s}) scale(${s})`} className="sc-tin">
        <path d="M11.5 9V6.5a3 3 0 0 1 3-3h3a3 3 0 0 1 3 3V9" className="sc-free" />
        <circle cx={4.6 - reach} cy={handY} r="1.9" className="sc-hand" />
        <circle cx={27.4 + reach} cy={handY} r="1.9" className={`sc-hand${wave ? " sc-wave" : ""}`} />
        <path d="M7 12q0-3.2 9-3.2t9 3.2v13q0 3.5-9 3.5t-9-3.5z" className="sc-body" />
        <path d="M7 12.6q9 2.6 18 0M7 17.4q9 2.6 18 0M7 24.2q9 2.6 18 0" />
        <circle cx="12.6" cy="21" r="1.15" className="sc-ink" />
        <circle cx="19.4" cy="21" r="1.15" className="sc-ink" />
        {face === "smile" && <path d="M14.9 22q1.1 1 2.2 0" />}
        {face === "grin" && <path d="M14.5 21.9q1.5 1.9 3 0z" className="sc-ink" />}
        {face === "flat" && <path d="M15 22.6h2" />}
      </g>
    </g>
  );
}

/** A one-unit rack server. */
function Server({
  x,
  y,
  w = 120,
  h = 24,
  led = "on",
  className,
  delay,
}: {
  x: number;
  y: number;
  w?: number;
  h?: number;
  led?: "on" | "blink" | "off";
  className?: string;
  delay?: number;
}) {
  return (
    <g className={className} style={delay ? { animationDelay: `${delay}s` } : undefined}>
      <rect x={x} y={y} width={w} height={h} rx="4" className="sc-body" />
      <path d={`M${x + 12} ${y + h / 2 - 4}h26M${x + 12} ${y + h / 2}h26M${x + 12} ${y + h / 2 + 4}h26`} className="sc-thin" />
      <circle
        cx={x + w - 34}
        cy={y + h / 2}
        r="3"
        className={led === "off" ? "sc-soft" : `sc-brass${led === "blink" ? " sc-blink" : ""}`}
      />
      <circle cx={x + w - 23} cy={y + h / 2} r="3" className="sc-soft" />
      <circle cx={x + w - 12} cy={y + h / 2} r="3" className="sc-soft" />
    </g>
  );
}

const Ground = ({ x1 = 24, x2 = 296, y = 150 }: { x1?: number; x2?: number; y?: number }) => (
  <path d={`M${x1} ${y}H${x2}`} className="sc-ground" />
);

function Hello() {
  return (
    <>
      <Ground />
      <Tin cx={88} ground={150} s={3.2} wave />
      <g className="sc-float">
        <path
          transform="translate(186 12) scale(4.6)"
          d="M17.5 19H9a7 7 0 1 1 6.71-9h1.79a4.5 4.5 0 1 1 0 9Z"
          className="sc-soft-fill sc-ns"
        />
      </g>
      <circle cx="152" cy="74" r="3.2" className="sc-brass sc-dot" />
      <circle cx="170" cy="68" r="3.2" className="sc-brass sc-dot" style={{ animationDelay: "0.25s" }} />
      <circle cx="188" cy="62" r="3.2" className="sc-brass sc-dot" style={{ animationDelay: "0.5s" }} />
    </>
  );
}

function Build() {
  return (
    <>
      <Ground />
      <Tin cx={78} ground={150} s={2.9} bob />
      <Server x={166} y={124} className="sc-drop" />
      <Server x={166} y={98} className="sc-drop" led="blink" delay={0.5} />
      <Server x={166} y={72} className="sc-drop" delay={1} />
    </>
  );
}

function Address({ name }: { name: string }) {
  const label = name.length > 13 ? `${name.slice(0, 12)}…` : name;
  return (
    <>
      <Ground />
      <Tin cx={58} ground={150} s={2.6} />
      <path d="M140 150V46" className="sc-free" />
      <g className="sc-swing">
        <path d="M118 52h44M124 52v8M156 52v8" className="sc-thin sc-free" />
        <rect x="100" y="60" width="80" height="28" rx="5" className="sc-paper" />
        <text x="140" y="79" textAnchor="middle" className="sc-label">
          {label}
        </text>
      </g>
      <path d="M182 74q48 4 60 46" className="sc-path" />
      <Server x={198} y={126} w={100} led="blink" />
    </>
  );
}

function Pack() {
  // A bigger tin of its own, so its lid can lift.
  return (
    <>
      <Ground />
      <g className="sc-item" style={{ animationDelay: "0.4s" }}>
        <rect x="142" y="92" width="18" height="16" rx="2" className="sc-brass" />
        <path d="M151 92v16" className="sc-thin" />
      </g>
      <g className="sc-item" style={{ animationDelay: "0.9s" }}>
        <rect x="163" y="90" width="14" height="20" rx="7" className="sc-soft-fill" />
      </g>
      <g className="sc-item" style={{ animationDelay: "1.4s" }}>
        <rect x="150" y="88" width="16" height="22" rx="2" className="sc-paper" />
        <path d="M154 95h8M154 100h8M154 105h5" className="sc-thin" />
      </g>
      <circle cx="113" cy="122" r="6" className="sc-hand" />
      <circle cx="207" cy="122" r="6" className="sc-hand" />
      <path d="M118 84v52q0 12 42 12t42-12V84" className="sc-body" />
      <path d="M118 84q42 12 84 0" className="sc-ns" />
      <path d="M118 102q42 10 84 0M118 128q42 10 84 0" />
      <circle cx="146" cy="116" r="3.6" className="sc-ink" />
      <circle cx="174" cy="116" r="3.6" className="sc-ink" />
      <path d="M155 120q5 4 10 0" />
      <g className="sc-lid">
        <path d="M114 84q0-14 46-14t46 14q-46 10-92 0z" className="sc-body" />
        <path d="M144 70v-10a8 8 0 0 1 8-8h16a8 8 0 0 1 8 8v10" className="sc-free" />
      </g>
    </>
  );
}

function Lock() {
  return (
    <>
      <Ground />
      <Tin cx={74} ground={150} s={2.9} bob />
      <g className="sc-shackle">
        <path d="M184 96V76a24 24 0 0 1 48 0v20" className="sc-thick sc-free" />
      </g>
      <rect x="170" y="94" width="76" height="56" rx="10" className="sc-body" />
      <circle cx="208" cy="116" r="6" className="sc-ink" />
      <path d="M208 118v14" className="sc-thick" />
      <circle cx="208" cy="122" r="44" className="sc-ring" />
    </>
  );
}

function Cert({ waiting }: { waiting?: boolean }) {
  return (
    <>
      <Ground />
      <Tin cx={58} ground={150} s={2.6} face={waiting ? "flat" : "smile"} />
      <rect x="116" y="34" width="184" height="116" rx="10" className="sc-paper" />
      <path d="M116 56h184" className="sc-thin" />
      <circle cx="130" cy="45" r="3" className="sc-soft" />
      <circle cx="141" cy="45" r="3" className="sc-soft" />
      <circle cx="152" cy="45" r="3" className="sc-soft" />
      <rect x="130" y="66" width="156" height="18" rx="9" className="sc-soft-fill" />
      <rect x="139" y="74" width="9" height="7" rx="1.5" className={waiting ? "sc-ink" : "sc-brass"} />
      <path d="M140.5 74v-2a3 3 0 0 1 6 0v2" className="sc-thin" />
      <path d="M156 75h70" className="sc-thin" />
      <path d="M132 100h70M132 112h56M132 124h64" className="sc-thin" />
      {waiting ? (
        <g>
          <circle cx="254" cy="114" r="20" className="sc-paper" />
          <path d="M254 114v-12" className="sc-thick sc-spin" />
          <path d="M254 114h8" className="sc-thick" />
        </g>
      ) : (
        <g className="sc-stamp">
          <path d="M244 126l-8 22 9-5 5 8 4-21zM264 126l8 22-9-5-5 8-4-21z" className="sc-brass" />
          <circle cx="254" cy="114" r="20" className="sc-brass" />
          <circle cx="254" cy="114" r="13" className="sc-thin" strokeDasharray="3 3" />
          <path d="M247 114l5 5 9-10" className="sc-thick" />
        </g>
      )}
    </>
  );
}

const SPARKS: [number, number, string][] = [
  [92, 52, "sc-brass"],
  [228, 48, "sc-leaf"],
  [74, 96, "sc-teal"],
  [246, 92, "sc-brass"],
  [116, 22, "sc-plum"],
  [206, 18, "sc-brass"],
];

function Ready() {
  return (
    <>
      <Ground x1={60} x2={260} />
      <Server x={98} y={126} w={124} />
      <Tin cx={160} ground={126} s={3} face="grin" cheer />
      {SPARKS.map(([x, y, c], i) => (
        <path
          key={i}
          d={`M${x} ${y - 7}q1.5 5.5 7 7q-5.5 1.5-7 7q-1.5-5.5-7-7q5.5-1.5 7-7z`}
          className={`${c} sc-spark sc-ns`}
          style={{ animationDelay: `${0.15 + i * 0.07}s` }}
        />
      ))}
    </>
  );
}

function Stopped() {
  return (
    <>
      <Ground />
      <Tin cx={104} ground={150} s={3} face="flat" />
      <Server x={176} y={126} w={110} led="off" />
      <path d="M286 138q18 0 10-16t8-16" className="sc-thin sc-free" />
    </>
  );
}

export function Scene({ id, name, className }: { id: SceneId; name: string; className?: string }) {
  return (
    <svg viewBox="0 0 320 166" className={`sc ${className ?? ""}`} data-scene={id} aria-hidden="true" focusable="false">
      <g className="sc-art">
        {id === "hello" && <Hello />}
        {id === "build" && <Build />}
        {id === "address" && <Address name={name} />}
        {id === "pack" && <Pack />}
        {id === "lock" && <Lock />}
        {id === "cert" && <Cert />}
        {id === "waiting" && <Cert waiting />}
        {id === "ready" && <Ready />}
        {id === "stopped" && <Stopped />}
      </g>
    </svg>
  );
}
