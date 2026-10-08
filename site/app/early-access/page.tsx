import { redirect } from "next/navigation";

// The old early-access address; the list lives on /start now.
export default async function EarlyAccess({ searchParams }: { searchParams: Promise<{ error?: string }> }) {
  const { error } = await searchParams;
  redirect(error ? `/start?error=${encodeURIComponent(error)}#form` : "/start");
}
