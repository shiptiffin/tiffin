"use server";

import { redirect } from "next/navigation";
import { signIn, signOut, signUp } from "tiffin-sdk/next/auth";

// Plain forms (they work without JavaScript): failures come back in the URL.
export async function signUpAction(form: FormData) {
  const r = await signUp(form, { redirectTo: "/dashboard" });
  if (!r.ok) redirect(`/sign-in?error=${r.code}`);
}

export async function signInAction(form: FormData) {
  const r = await signIn(form, { redirectTo: "/dashboard" });
  if (!r.ok) redirect(`/sign-in?error=${r.code}`);
}

export async function signOutAction() {
  await signOut({ redirectTo: "/sign-in" });
}
