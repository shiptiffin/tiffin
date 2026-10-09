import { describe, expect, test } from "bun:test";
import { DELETE_STAGES, duration, estimate, fraction, readDeleteSteps, readSteps, secondsLeft, STAGES, type Step } from "./progress";
import { SAMPLE_START, sampleDeleteSteps, sampleIndex, sampleSteps } from "./progress-sample";

const at = (s: number) => SAMPLE_START + s * 1000;
const upTo = (prefix: string) => sampleSteps(sampleIndex(prefix) + 1);
const stageOf = (steps: Step[], status = "provisioning") => STAGES[readSteps(steps, status).stage].id;

describe("progress stages", () => {
  test("each raw step lands in its stage", () => {
    expect(stageOf([])).toBe("hello");
    expect(stageOf(upTo("Checking your Hetzner project"))).toBe("hello");
    expect(stageOf(upTo("creating the firewall"))).toBe("build");
    expect(stageOf(upTo("Pointing"))).toBe("address");
    expect(stageOf(upTo("Waiting for the server to start"))).toBe("address");
    expect(stageOf(upTo("formatting"))).toBe("pack"); // an unmatched line stays where it was
    expect(stageOf(upTo("provisioning system services"))).toBe("pack");
    expect(stageOf(upTo("Removing the setup key"))).toBe("lock");
    expect(stageOf(upTo("Forgot your Hetzner key"))).toBe("lock");
    expect(stageOf(upTo("Waiting for https://"), "cert_pending")).toBe("cert");
  });

  test("friendly lines, never calling our SSH access a key", () => {
    expect(readSteps(upTo("Creating a cx23"), "provisioning").say).toBe("Ordering a cx23 server in Falkenstein");
    expect(readSteps(upTo("creating the 40 GB"), "provisioning").say).toBe("Adding a 40 GB data disk");
    expect(readSteps(upTo("Pointing"), "provisioning").say).toBe("Pointing acme.shiptiffin.app at it");
    expect(readSteps(upTo("provisioning system services"), "provisioning").say).toMatch(/system packages/);
    const lock = readSteps(upTo("Removing the setup key"), "provisioning").say;
    expect(lock).toBe("Removing our login from your server");
    for (const s of STAGES) expect(`${s.title} ${s.blurb}`).not.toMatch(/\bkey\b/i);
    // An older job that kept the token: the same last step.
    const old = [
      ...upTo("Closing SSH"),
      { at: "2026-10-08T12:04:15Z", text: "Kept your Hetzner key, sealed, for one-click resizes (remove it any time)" },
    ];
    expect(readSteps(old, "provisioning").say).toBe("Forgetting your Hetzner API token");
  });

  test("a reworded or unknown step never moves backwards", () => {
    const steps = [
      ...upTo("installing the service"),
      { at: "2026-10-08T12:01:50Z", text: "Checking your Hetzner project" },
      { at: "2026-10-08T12:01:51Z", text: "something new" },
    ];
    expect(stageOf(steps)).toBe("pack");
  });

  test("the box status knows more than the steps", () => {
    expect(stageOf(upTo("Closing SSH"), "cert_pending")).toBe("cert");
    expect(readSteps(upTo("Pointing"), "active").ready).toBe(true);
  });

  test("certificate pending is told apart from the usual wait", () => {
    const wait = upTo("Waiting for https://");
    expect(readSteps(wait, "cert_pending").certWaiting).toBe(false);
    const pending = [
      ...wait,
      {
        at: "2026-10-08T12:09:20Z",
        text: "Installed. The certificate is still pending (timeout): we check every minute and email you when the dashboard is ready",
      },
    ];
    const r = readSteps(pending, "cert_pending");
    expect(r.certWaiting).toBe(true);
    expect(estimate(r, at(600))).toBe("Checking every minute");
  });

  test("a stopped setup keeps the stage it reached", () => {
    const steps = [
      ...upTo("Pointing"),
      { at: "2026-10-08T12:00:50Z", text: "Setup stopped: set the DNS records: 502" },
      { at: "2026-10-08T12:00:52Z", text: "Deleting what this setup made in your Hetzner project" },
    ];
    const r = readSteps(steps, "failed");
    expect(r.stopped).toBe(true);
    expect(STAGES[r.stage].id).toBe("address");
  });

  test("the estimate is honest and rounded", () => {
    const start = readSteps(sampleSteps(1), "provisioning");
    expect(estimate(start, at(1))).toBe("About 5 minutes left");
    const pack = readSteps(upTo("provisioning system services"), "provisioning");
    expect(estimate(pack, at(120))).toBe("About 3 minutes left");
    expect(estimate(pack, at(600))).toBe("Taking a little longer than usual");
    const cert = readSteps(upTo("Waiting for https://"), "cert_pending");
    expect(estimate(cert, at(260))).toBe("Almost there");
    // never zero while running, and the bar never reaches the end before ready
    expect(secondsLeft(pack, at(10_000))).toBeGreaterThan(0);
    expect(fraction(pack, at(10_000))).toBeLessThan(1);
    expect(fraction(readSteps(sampleSteps(), "active"), at(300))).toBe(1);
  });

  test("the bar only goes forward", () => {
    const all = sampleSteps();
    let last = 0;
    for (let n = 1; n <= all.length; n++) {
      const f = fraction(readSteps(all.slice(0, n), "provisioning"), Date.parse(all[n - 1].at));
      expect(f).toBeGreaterThanOrEqual(last);
      last = f;
    }
  });

  test("durations", () => {
    expect(duration(291_000)).toBe("4 min 51 s");
    expect(duration(42_000)).toBe("42 s");
  });
});

test("installed but the last steps failed: flagged for a person", () => {
  const steps = [
    ...upTo("Removing the setup key"),
    {
      at: "2026-10-08T12:04:10Z",
      text: "Tiffin is installed, but the last setup steps didn't finish (x). Your server and its data are kept; we look at it and email you.",
    },
  ];
  const r = readSteps(steps, "provisioning");
  expect(r.attention).toBe(true);
  expect(r.stopped).toBe(false);
});

describe("delete stages", () => {
  const stage = (n: number, status = "deleting") => DELETE_STAGES[readDeleteSteps(sampleDeleteSteps(n), status).stage].id;
  test("each step of a delete lands in its stage, and only the end says deleted", () => {
    expect(stage(0)).toBe("address");
    expect(stage(1)).toBe("address");
    expect(stage(2)).toBe("server");
    expect(stage(3)).toBe("server");
    expect(stage(4)).toBe("cleanup");
    expect(stage(8)).toBe("cleanup");
    expect(stage(9)).toBe("deleted");
    expect(readDeleteSteps(sampleDeleteSteps(8), "deleting").done).toBe(false);
  });
  test("the box's status wins: deleted is done, whatever steps were read", () => {
    expect(readDeleteSteps([], "deleted")).toMatchObject({ done: true, stage: DELETE_STAGES.length - 1 });
  });
  test("an unknown step never moves it backwards", () => {
    const steps = [...sampleDeleteSteps(3), { at: "2026-10-08T12:00:14Z", text: "something new" }, { at: "2026-10-08T12:00:15Z", text: "Removing acme.shiptiffin.app first" }];
    expect(DELETE_STAGES[readDeleteSteps(steps, "deleting").stage].id).toBe("server");
  });
});
