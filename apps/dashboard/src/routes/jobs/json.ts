// The JSON editors' check: where a payload first goes wrong, in words.

/** "" is no payload; anything else must be JSON. The message names the line and column. */
export function jsonError(s: string): string | null {
  if (!s.trim()) return null;
  try {
    JSON.parse(s);
    return null;
  } catch {
    // Browsers word (and place) JSON errors differently, so find the spot ourselves.
    const { at, want } = jsonSpot(s);
    const before = s.slice(0, at);
    const line = before.split("\n").length;
    const col = before.length - before.lastIndexOf("\n");
    return `That isn’t JSON yet (line ${line}, column ${col}): ${want}.`;
  }
}

/** Where a JSON text first goes wrong, and what belongs there. */
function jsonSpot(s: string): { at: number; want: string } {
  let i = 0;
  class Stop {
    constructor(public want: string) {}
  }
  const ws = () => {
    while (i < s.length && " \t\n\r".includes(s[i])) i++;
  };
  const fail = (want: string): never => {
    throw new Stop(want);
  };
  const str = () => {
    i++;
    while (i < s.length && s[i] !== '"') {
      if (s[i] === "\n") fail("a closing quote before the line ends");
      i += s[i] === "\\" ? 2 : 1;
    }
    if (i >= s.length) fail("a closing quote");
    i++;
  };
  const value = (): void => {
    ws();
    const c = s[i];
    if (c === "{") {
      i++;
      ws();
      if (s[i] === "}") return void i++;
      for (;;) {
        ws();
        if (s[i] !== '"') fail(`a "quoted" name`);
        str();
        ws();
        if (s[i] !== ":") fail("a colon after the name");
        i++;
        value();
        ws();
        if (s[i] === ",") {
          i++;
          continue;
        }
        if (s[i] === "}") return void i++;
        fail("a comma or a closing }");
      }
    }
    if (c === "[") {
      i++;
      ws();
      if (s[i] === "]") return void i++;
      for (;;) {
        value();
        ws();
        if (s[i] === ",") {
          i++;
          continue;
        }
        if (s[i] === "]") return void i++;
        fail("a comma or a closing ]");
      }
    }
    if (c === '"') return str();
    const m = s.slice(i).match(/^(-?\d+(\.\d+)?([eE][+-]?\d+)?|true|false|null)/);
    if (!m) fail(c === undefined ? "a value" : "a value (text in \"quotes\", a number, true, false, null, {…} or […])");
    i += m![0].length;
  };
  try {
    value();
    ws();
    if (i < s.length) fail("the end here");
  } catch (e) {
    if (e instanceof Stop) return { at: Math.min(i, s.length), want: `expected ${e.want}` };
    throw e;
  }
  return { at: s.length, want: "expected a value" };
}
