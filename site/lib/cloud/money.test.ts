import { expect, test } from "bun:test";
import { moneyFacts } from "./billing";
import { dayWords, endedWords, lastCharge, money, priceWords } from "./money";

const now = new Date("2026-10-09T12:00:00Z");
const row = {
  founding: true,
  first_paid_at: new Date("2026-10-09T00:40:00Z"),
  refunded_at: null,
  last_charge_cents: null,
  last_charge_currency: null,
  last_charge_at: null,
  plan_ended_at: null,
};

test("prices and charges read plainly", () => {
  expect(money(1200)).toBe("$12");
  expect(money(1950)).toBe("$19.50");
  expect(priceWords(true)).toBe("$12 a month");
  expect(priceWords(false)).toBe("$19 a month");
  expect(dayWords(new Date("2026-11-08T00:00:00Z"), now)).toBe("8 Nov");
  expect(dayWords(new Date("2027-01-08T00:00:00Z"), now)).toBe("8 Jan 2027");
});

test("a deleted box: cancelled on, last charged on (from the recorded invoice, else the first month)", () => {
  const deleted = new Date("2026-10-09T00:57:00Z");
  expect(endedWords(row, deleted, now)).toBe("Subscription cancelled on 9 Oct · last charged $12 on 9 Oct");
  const recorded = { ...row, last_charge_cents: 1900, last_charge_currency: "usd", last_charge_at: new Date("2026-11-09T00:40:00Z"), plan_ended_at: new Date("2026-11-20T10:00:00Z") };
  expect(endedWords(recorded, deleted, now)).toBe("Subscription cancelled on 20 Nov · last charged $19 on 9 Nov");
  expect(endedWords({ ...row, refunded_at: now }, deleted, now)).toContain("(refunded)");
  expect(lastCharge({ ...row, first_paid_at: null })).toBeNull();
});

test("the money facts come from the subscription Stripe sends (its expanded latest invoice)", () => {
  const sub = {
    id: "sub_1",
    status: "canceled",
    customer: "cus_1",
    canceled_at: 1_791_506_220,
    ended_at: 1_791_506_220,
    latest_invoice: { id: "in_1", status: "paid", amount_paid: 1200, currency: "usd", status_transitions: { paid_at: 1_791_504_000 } },
  };
  expect(moneyFacts(sub)).toEqual({ lastCharge: { cents: 1200, currency: "usd", at: new Date(1_791_504_000_000) }, planEndedAt: new Date(1_791_506_220_000) });
  expect(moneyFacts({ ...sub, status: "active", latest_invoice: { id: "in_2", status: "open" } })).toEqual({});
});
