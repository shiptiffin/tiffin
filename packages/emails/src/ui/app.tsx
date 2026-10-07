// What every app email shares: the app's own name, logo and accent. Nothing
// of ShipTiffin's shows.
import type { ReactNode } from "react";
import { Link } from "react-email";
import { v, when, whenInline } from "./compile";
import { AppLayout, LogoCell } from "./layout";
import { Cta as BaseCta, Small } from "./parts";

export const appVars = {
  app: v.text("The app's name"),
  logoUrl: v.optUrl("A square logo, PNG or JPEG over https, shown at 32x32; empty for none"),
  accent: v.color("The button colour, #rrggbb"),
  accentText: v.color("Text on the button, #rrggbb: white or near-black, whichever reads better on the accent"),
  site: v.optUrl("The app's address, for the footer; empty to leave it out"),
  siteLabel: v.optText('How the address is shown: "shop.example.com"'),
  email: v.text("The address this email went to"),
};

export type AppBase = { app: string; logoUrl: string; accent: string; accentText: string; site: string; siteLabel: string; email: string };

export const appSample: AppBase = {
  app: "Larder",
  logoUrl: "",
  accent: "#2f6b4f",
  accentText: "#ffffff",
  site: "https://larder.app",
  siteLabel: "larder.app",
  email: "sam@example.com",
};

export function App<P extends AppBase>({ p, preview, why, children }: { p: P; preview: string; why: ReactNode; children: ReactNode }) {
  return (
    <AppLayout
      preview={preview}
      app={p.app}
      logo={when(p, "logoUrl", <LogoCell src={p.logoUrl} />)}
      why={<Small>{why}</Small>}
      site={
        <Small last>
          {p.app}
          {whenInline(
            p,
            "site",
            <>
              {" · "}
              <Link href={p.site} className="tf-ink3 tf-plain text-ink-3 underline">
                {p.siteLabel}
              </Link>
            </>,
          )}
        </Small>
      }
    >
      {children}
    </AppLayout>
  );
}

/** The app's button, in its accent. */
export function AppCta<P extends AppBase>({ p, href, children }: { p: P; href: string; children: ReactNode }) {
  return (
    <BaseCta href={href} style={{ backgroundColor: p.accent, color: p.accentText }}>
      {children}
    </BaseCta>
  );
}
