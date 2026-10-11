// The things an app needs, drawn in the tin's line style (stroke and fills come from the
// classes .o .thin .body .lit .hollow .paper .brass .dot, set by whatever draws them).

/* Each icon is drawn around (0,0), about 56 across. */
export function Icon({ id }: { id: string }) {
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
