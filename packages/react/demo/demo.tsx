// A gallery of the components against a real engine (demo/server.ts), for
// eyeballing and screenshots: bun demo/server.ts, then open the printed URL.
// ?only=signin|signup|invite|accept|bar  ?theme=dark
import { createRoot } from "react-dom/client";
import { AcceptInvite, Invite, OrgSwitcher, SignIn, SignUp, UserButton } from "../src";

const q = new URLSearchParams(location.search);
const only = q.get("only");
if (q.get("theme") === "dark") document.documentElement.classList.add("dark");

function Frame({ name, children, wide }: { name: string; children: React.ReactNode; wide?: boolean }) {
  if (only && only !== name) return null;
  return (
    <div data-shot={name} style={{ display: "grid", placeItems: "center", padding: only ? "56px 16px" : "40px 16px", minWidth: wide ? 640 : 0 }}>
      {children}
    </div>
  );
}

function Bar() {
  return (
    <div style={{ width: "min(720px, 100%)", display: "flex", alignItems: "center", justifyContent: "space-between", padding: "10px 14px", borderRadius: 12, background: "var(--bar, transparent)" }}>
      <OrgSwitcher onSwitch={() => {}} />
      <UserButton />
    </div>
  );
}

function App() {
  const token = q.get("link") ?? (window as any).__LINK__;
  return (
    <div style={{ display: "flex", flexWrap: "wrap", justifyContent: "center", alignItems: "flex-start" }}>
      <Frame name="bar" wide>
        <Bar />
      </Frame>
      <Frame name="signin">
        <SignIn onSignedIn={() => {}} signUpUrl="#" logo={<Logo />} />
      </Frame>
      <Frame name="signup">
        <SignUp signInUrl="#" />
      </Frame>
      <Frame name="invite">
        <Invite />
      </Frame>
      <Frame name="accept">
        <AcceptInvite token={token} onAccepted={() => {}} />
      </Frame>
    </div>
  );
}

function Logo() {
  return (
    <svg viewBox="0 0 120 32" height="32" aria-label="Shop" role="img">
      <rect x="0" y="4" width="24" height="24" rx="7" fill="oklch(0.6 0.105 78)" />
      <path d="M7 16.5h10M12 11.5v10" stroke="white" strokeWidth="2.2" strokeLinecap="round" />
      <text x="32" y="23" fontFamily="Fraunces, Georgia, serif" fontSize="19" fontWeight="550" fill="currentColor">Shop</text>
    </svg>
  );
}

fetch("/demo/state").then(async (r) => {
  const s = await r.json();
  (window as any).__LINK__ = s.link;
  createRoot(document.getElementById("root")!).render(<App />);
});
