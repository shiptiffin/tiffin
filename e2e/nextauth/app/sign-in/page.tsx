import { signInAction, signUpAction } from "../actions";

export default async function SignInPage({ searchParams }: { searchParams: Promise<{ next?: string; error?: string }> }) {
  const { next = "", error } = await searchParams;
  return (
    <main>
      {error && <p id="error">{error}</p>}
      <form id="sign-up" action={signUpAction}>
        <input name="name" defaultValue="Ana" />
        <input name="email" type="email" />
        <input name="password" type="password" />
        <input name="captcha" type="hidden" />
        <input name="next" type="hidden" value={next} />
        <button type="submit">Create account</button>
      </form>
      <form id="sign-in" action={signInAction}>
        <input name="email" type="email" />
        <input name="password" type="password" />
        <input name="captcha" type="hidden" />
        <input name="next" type="hidden" value={next} />
        <button type="submit">Sign in</button>
      </form>
    </main>
  );
}
