import { currentUser } from "tiffin-sdk/next/auth";

export default async function Home() {
  const user = await currentUser();
  return <main id="home">{user ? `Hello ${user.name}` : "Signed out"}</main>;
}
