import { verifySession } from "@shiptiffin/sdk/next/auth";
import { signOutAction } from "../actions";

export default async function Dashboard() {
  const { user, organization } = await verifySession();
  return (
    <main>
      <p id="who">{user.email}</p>
      <p id="org">
        {organization?.name}:{organization?.role}
      </p>
      <form id="sign-out" action={signOutAction}>
        <button type="submit">Sign out</button>
      </form>
    </main>
  );
}
