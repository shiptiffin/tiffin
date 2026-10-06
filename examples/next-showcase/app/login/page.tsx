import type { Metadata } from "next";
import { login } from "../actions";

export const metadata: Metadata = { title: "Sign in" };

export default function Login() {
  return (
    <main>
      <h1>Sign in</h1>
      <p className="muted">There is no real account yet: this sets a session cookie the proxy checks.</p>
      <form action={login}>
        <button id="login">Continue as demo</button>
      </form>
    </main>
  );
}
