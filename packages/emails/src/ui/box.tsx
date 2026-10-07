// What every box email shares: who it's from and the mark.
import type { ReactNode } from "react";
import { v, when } from "./compile";
import { BoxLayout, MarkCell } from "./layout";
import { Small } from "./parts";

export const boxVars = {
  brand: v.text('"ShipTiffin" on shiptiffin.com, "Tiffin" on a self-hosted box'),
  host: v.text("The dashboard's host, e.g. dashboard.example.com"),
  markUrl: v.optUrl("The mark as a PNG (email-mark.png on the dashboard); empty for none"),
};

export type BoxBase = { brand: string; host: string; markUrl: string };

export const boxSample: BoxBase = {
  brand: "ShipTiffin",
  host: "dashboard.shiptiffin.com",
  markUrl: "https://dashboard.shiptiffin.com/email-mark.png",
};

/** `oneLine`: the footer is the one line "{why} · {brand}", without the dashboard's address. */
export function Box<P extends BoxBase>({ p, preview, why, oneLine, children }: { p: P; preview: string; why: ReactNode; oneLine?: boolean; children: ReactNode }) {
  return (
    <BoxLayout
      preview={preview}
      brand={p.brand}
      host={p.host}
      mark={when(p, "markUrl", <MarkCell src={p.markUrl} />)}
      why={<Small>{why}</Small>}
      footer={
        oneLine ? (
          <Small last>
            {why} · {p.brand}
          </Small>
        ) : undefined
      }
    >
      {children}
    </BoxLayout>
  );
}
