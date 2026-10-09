// The /start progress screen reads the provisioner's raw step texts
// (internal/cloud/worker.go, the Hetzner provider's Ensure and the install
// steps it shares with `tiffin up`) and groups them into a few plain stages.
// A step that matches no rule belongs to the stage of the step before it
// (the data disk's script lines, warnings), so a reworded step never moves
// the screen backwards; at worst it stays on the stage it was on.

export type StageId = "hello" | "build" | "address" | "pack" | "lock" | "cert";

export type Stage = {
  id: StageId;
  title: string;
  /** What happens in this stage, for when no step has said more yet. */
  blurb: string;
  /** Typical seconds, from the provisioner's usual run (about five minutes in all). */
  typical: number;
};

export const STAGES: Stage[] = [
  { id: "hello", title: "Saying hello to Hetzner", blurb: "Checking your Hetzner project is ready for a new box.", typical: 8 },
  {
    id: "build",
    title: "Building your server",
    blurb: "A new server, a firewall and a 40 GB data disk, in your own Hetzner project.",
    typical: 40,
  },
  { id: "address", title: "Giving it an address", blurb: "Pointing your name at the new server while it starts up.", typical: 45 },
  { id: "pack", title: "Packing the tin", blurb: "Installing Tiffin and everything it needs. This is the longest part.", typical: 150 },
  {
    id: "lock",
    title: "Locking the door behind us",
    blurb: "Our temporary SSH access is removed. After this, only you can get in.",
    typical: 12,
  },
  { id: "cert", title: "Getting its certificate", blurb: "So your dashboard opens over HTTPS.", typical: 40 },
];

export const TYPICAL_TOTAL = STAGES.reduce((n, s) => n + s.typical, 0);

type Rule = {
  re: RegExp;
  stage: StageId | "ready" | "stopped" | "attention" | "cleanup";
  say?: string | ((m: RegExpMatchArray) => string);
};

// Order matters: the first rule that matches wins.
const RULES: Rule[] = [
  { re: /^Setup stopped/, stage: "stopped" },
  { re: /^Tiffin is installed, but the last setup steps/, stage: "attention" },
  { re: /^(Deleting what this setup made|Deleted |Couldn't |Nothing to clean up|The box was installed meanwhile)/, stage: "cleanup" },

  { re: /^Finding the address this setup connects from/, stage: "hello", say: "Getting ready to call Hetzner" },
  { re: /^Checking your Hetzner project/, stage: "hello", say: "Checking your Hetzner project" },
  { re: /^Removing (?!the setup key)\S+ first/, stage: "hello", say: "Tidying up after an earlier try" },
  { re: /^Deleting what an earlier attempt/, stage: "hello", say: "Tidying up after an earlier try" },

  { re: /^Creating an? (\S+) server in (.+?) \(/, stage: "build", say: (m) => `Ordering a ${m[1]} server in ${m[2]}` },
  { re: /^(uploading|using) the SSH (public )?key/i, stage: "build", say: "Setting up temporary access for the setup" },
  { re: /^(creating|updating) the firewall/i, stage: "build", say: "Putting up the firewall" },
  { re: /^creating the (\d+) GB data volume/i, stage: "build", say: (m) => `Adding a ${m[1]} GB data disk` },
  { re: /^creating the server/i, stage: "build", say: "Switching on a fresh server" },
  { re: /^starting the server/i, stage: "build", say: "Switching the server on" },
  { re: /^attaching the data volume/i, stage: "build", say: "Plugging in the data disk" },

  { re: /^Pointing (\S+) at your server/, stage: "address", say: (m) => `Pointing ${m[1]} at it` },
  { re: /^Waiting for the server to start/, stage: "address", say: "Waiting for it to wake up" },
  { re: /^waiting for SSH/i, stage: "address", say: "Waiting for it to wake up" },

  { re: /^Fetching the signed Tiffin release/, stage: "pack", say: "Fetching the latest signed Tiffin" },
  { re: /^preparing the data disk/i, stage: "pack", say: "Setting up the data disk" },
  { re: /^copying tiffin/i, stage: "pack", say: "Copying Tiffin over" },
  { re: /^installing the service/i, stage: "pack", say: "Installing Tiffin" },
  { re: /^provisioning system services/i, stage: "pack", say: "Installing system packages, the slow bit" },
  { re: /^starting tiffin/i, stage: "pack", say: "Starting Tiffin for the first time" },
  { re: /^Making your one-time sign-in link/, stage: "pack", say: "Making your one-time sign-in link" },

  { re: /^Removing the setup key from your server/, stage: "lock", say: "Removing our login from your server" },
  { re: /^Closing SSH/, stage: "lock", say: "Closing SSH in the firewall" },
  // Older jobs, from when a token could be kept: the same final step.
  { re: /^Kept your Hetzner (key|API token)/, stage: "lock", say: "Forgetting your Hetzner API token" },
  { re: /^Forgot your Hetzner (key|API token)/, stage: "lock", say: "Forgetting your Hetzner API token" },

  { re: /^Waiting for \S+ to answer over HTTPS/, stage: "cert", say: "Asking Let's Encrypt for a certificate" },
  { re: /^Installed\. The certificate is still pending/, stage: "cert", say: "The certificate is still on its way" },
  { re: /^The dashboard answers/, stage: "cert", say: "Double-checking the dashboard answers" },

  { re: /^Your box is ready/, stage: "ready" },
];

export type Step = { at: string; text: string };

export type Reading = {
  /** Index into STAGES of the stage now running (or where it stopped). */
  stage: number;
  /** The friendly line for the latest step that has one. */
  say: string;
  /** When each stage started (ms), where known. */
  startedAt: (number | null)[];
  /** When the first step was written (ms). */
  firstAt: number | null;
  lastAt: number | null;
  /** The worker said the certificate is still pending and handed over to the check. */
  certWaiting: boolean;
  /** The worker said "Your box is ready". */
  ready: boolean;
  /** The setup stopped (a "Setup stopped" step). */
  stopped: boolean;
  /** Tiffin is installed but the last steps failed: kept, and a person looks at it. */
  attention: boolean;
};

const index = (id: StageId) => STAGES.findIndex((s) => s.id === id);

/** Group the raw steps into stages. `status` is the box's status. */
export function readSteps(steps: Step[], status: string): Reading {
  const r: Reading = {
    stage: 0,
    say: "",
    startedAt: STAGES.map(() => null),
    firstAt: null,
    lastAt: null,
    certWaiting: false,
    ready: false,
    stopped: false,
    attention: false,
  };
  let sayStage = -1;
  for (const s of steps) {
    const at = Date.parse(s.at);
    const t = Number.isFinite(at) ? at : null;
    if (t != null) {
      r.firstAt ??= t;
      r.lastAt = t;
    }
    const rule = RULES.find((x) => x.re.test(s.text));
    if (!rule) continue;
    if (rule.stage === "stopped") {
      r.stopped = true;
      continue;
    }
    if (rule.stage === "attention") {
      r.attention = true;
      continue;
    }
    if (rule.stage === "cleanup") continue;
    if (rule.stage === "ready") {
      r.ready = true;
      continue;
    }
    const i = index(rule.stage);
    if (i < r.stage) continue; // never backwards
    for (let j = r.stage + 1; j <= i; j++) r.startedAt[j] ??= t;
    if (i === 0) r.startedAt[0] ??= t;
    r.stage = i;
    if (rule.say) {
      const m = s.text.match(rule.re)!;
      r.say = typeof rule.say === "function" ? rule.say(m) : rule.say;
      sayStage = i;
    }
    if (/^Installed\. The certificate is still pending/.test(s.text)) r.certWaiting = true;
  }
  r.startedAt[0] ??= r.firstAt;
  // The box's status knows more than a step that may not have been read yet.
  if (status === "cert_pending" && r.stage < index("cert")) {
    for (let j = r.stage + 1; j <= index("cert"); j++) r.startedAt[j] ??= r.lastAt;
    r.stage = index("cert");
  }
  if (status === "active") r.ready = true;
  if (sayStage !== r.stage) r.say = "";
  return r;
}

/** How far along, 0..1, counting each stage by its typical length. */
export function fraction(r: Reading, now: number): number {
  if (r.ready) return 1;
  let done = 0;
  for (let i = 0; i < r.stage; i++) done += STAGES[i].typical;
  const cur = STAGES[r.stage];
  const start = r.startedAt[r.stage];
  const spent = start == null ? 0 : Math.max(0, (now - start) / 1000);
  // The current stage fills to 90% at its typical length, then creeps.
  const part = spent <= cur.typical ? (spent / cur.typical) * 0.9 : 0.9 + 0.08 * (1 - cur.typical / spent);
  return Math.min(0.99, (done + part * cur.typical) / TYPICAL_TOTAL);
}

/** Seconds likely left: the rest of this stage (at least a little) and the typical length of those after it. */
export function secondsLeft(r: Reading, now: number): number {
  if (r.ready) return 0;
  const cur = STAGES[r.stage];
  const start = r.startedAt[r.stage];
  const spent = start == null ? 0 : Math.max(0, (now - start) / 1000);
  let left = Math.max(cur.typical - spent, cur.typical * 0.2);
  for (let i = r.stage + 1; i < STAGES.length; i++) left += STAGES[i].typical;
  return left;
}

/** A plain, rounded estimate: never more precise than it can be. */
export function estimate(r: Reading, now: number): string {
  if (r.ready) return "Done";
  if (r.certWaiting) return "Checking every minute";
  const cur = STAGES[r.stage];
  const start = r.startedAt[r.stage];
  const spent = start == null ? 0 : (now - start) / 1000;
  if (spent > cur.typical * 2.5 && spent > 60) return "Taking a little longer than usual";
  const s = secondsLeft(r, now);
  if (s < 45) return "Almost there";
  if (s < 90) return "About a minute left";
  return `About ${Math.round(s / 60)} minutes left`;
}

/** "4 min 52 s", for how long it took. */
export function duration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  const m = Math.floor(s / 60);
  return m ? `${m} min ${s % 60} s` : `${s} s`;
}

// ---- deleting -------------------------------------------------------------
// The account page's "being deleted" card reads the delete_server job's
// steps (internal/cloud/worker.go deleteServer, and the Hetzner provider's
// DestroyAll) the same way: a few plain stages, never backwards.

export type DeleteStageId = "address" | "server" | "cleanup" | "deleted";

export const DELETE_STAGES: { id: DeleteStageId; title: string; blurb: string; typical: number }[] = [
  { id: "address", title: "Removing the address", blurb: "Your name comes off first, so it never points at an IP Hetzner may give to someone else.", typical: 4 },
  { id: "server", title: "Deleting the server", blurb: "Only what carries this box's shiptiffin-box label, in your Hetzner project.", typical: 10 },
  { id: "cleanup", title: "Cleaning up", blurb: "Its firewall and the rest of what we made for it.", typical: 5 },
  { id: "deleted", title: "Deleted", blurb: "", typical: 0 },
];

const DELETE_RULES: { re: RegExp; stage: DeleteStageId }[] = [
  { re: /^Removing \S+ first/, stage: "address" },
  { re: /^(Deleting the server in your Hetzner project|removing delete protection|deleting the server )/i, stage: "server" },
  { re: /^(deleting the firewall|deleting the data volume|Deleted |Kept )/i, stage: "cleanup" },
  { re: /^Deleted$/, stage: "deleted" },
];

export type DeleteReading = {
  /** Index into DELETE_STAGES. */
  stage: number;
  done: boolean;
  firstAt: number | null;
  lastAt: number | null;
};

/** Where a delete is. `status` is the box's; `deleted` means done whatever the steps say. */
export function readDeleteSteps(steps: Step[], status: string): DeleteReading {
  const r: DeleteReading = { stage: 0, done: false, firstAt: null, lastAt: null };
  for (const s of steps) {
    const at = Date.parse(s.at);
    if (Number.isFinite(at)) {
      r.firstAt ??= at;
      r.lastAt = at;
    }
    const rule = DELETE_RULES.find((x) => x.re.test(s.text));
    if (!rule) continue;
    r.stage = Math.max(r.stage, DELETE_STAGES.findIndex((x) => x.id === rule.stage));
  }
  if (status === "deleted") r.stage = DELETE_STAGES.length - 1;
  r.done = r.stage === DELETE_STAGES.length - 1;
  return r;
}
