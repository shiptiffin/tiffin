// The money words the account page and the emails share: the price, the
// last charge and when the subscription ended. Pure: from the box's row
// (filled from the Stripe events we receive), never a call to Stripe.

/** The monthly price in cents: the founding price, or the standard one. */
export const monthlyCents = (founding: boolean) => (founding ? 1200 : 1900);

/** "$12", "$19.50", "€12". */
export function money(cents: number, currency = "usd"): string {
  const whole = cents % 100 === 0;
  return new Intl.NumberFormat("en-US", { style: "currency", currency: currency.toUpperCase(), minimumFractionDigits: whole ? 0 : 2 }).format(cents / 100);
}

/** "$12 a month" */
export const priceWords = (founding: boolean) => `${money(monthlyCents(founding))} a month`;

/** "9 Oct", with the year when it isn't this year's. */
export function dayWords(d: Date, now = new Date()): string {
  const year = d.getUTCFullYear() !== now.getUTCFullYear();
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "short", ...(year ? { year: "numeric" } : {}), timeZone: "UTC" });
}

export type MoneyRow = {
  founding: boolean;
  first_paid_at: Date | null;
  refunded_at: Date | null;
  last_charge_cents: number | null;
  last_charge_currency: string | null;
  last_charge_at: Date | null;
  plan_ended_at: Date | null;
};

/** The last charge: the latest paid invoice we recorded; for a box paid before we recorded it, its first month at its price. */
export function lastCharge(b: MoneyRow): { amount: string; at: Date } | null {
  if (b.last_charge_cents && b.last_charge_at) return { amount: money(b.last_charge_cents, b.last_charge_currency ?? "usd"), at: b.last_charge_at };
  if (b.first_paid_at) return { amount: money(monthlyCents(b.founding)), at: b.first_paid_at };
  return null;
}

/** "Subscription cancelled on 9 Oct · last charged $12 on 9 Oct". `endedFallback`: when we stopped it, if Stripe's date isn't in yet. */
export function endedWords(b: MoneyRow, endedFallback: Date | null, now = new Date()): string {
  const end = b.plan_ended_at ?? endedFallback;
  const charge = lastCharge(b);
  const parts = [end ? `Subscription cancelled on ${dayWords(end, now)}` : "Subscription cancelled"];
  if (charge) parts.push(`last charged ${charge.amount} on ${dayWords(charge.at, now)}${b.refunded_at ? " (refunded)" : ""}`);
  return parts.join(" · ");
}

/**
 * What ending the subscription now means for money, in the delete dialog and
 * the deleted email. Never a refund on delete: the 14-day
 * money-back guarantee is a refund an admin makes on request (/admin).
 */
export function endsNowWords(price: string | null): { title: string; body: string } {
  if (!price) return { title: "Your ShipTiffin subscription has already ended.", body: "Nothing more is billed." };
  return { title: `Deleting cancels your ShipTiffin subscription (${price}) now.`, body: "You won't be charged again, and the current month isn't refunded." };
}
export const GUARANTEE = "Within 14 days of your first payment, ask us at hello@shiptiffin.com for the money-back guarantee.";
export const NO_REFUND = `Payments already made aren't refunded. ${GUARANTEE}`;
