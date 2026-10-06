import { ImageResponse } from "next/og";

export const alt = "Showcase on Tiffin";
export const size = { width: 1200, height: 630 };
export const contentType = "image/png";

export default function OpengraphImage() {
  return new ImageResponse(
    (
      <div
        style={{
          width: "100%",
          height: "100%",
          display: "flex",
          flexDirection: "column",
          justifyContent: "flex-end",
          padding: 72,
          background: "linear-gradient(135deg, #2f5d50, #c9b88f)",
          color: "#fbfaf7",
        }}
      >
        <div style={{ fontSize: 88, fontWeight: 700 }}>Showcase</div>
        <div style={{ fontSize: 40 }}>Next.js 16 on one Tiffin box</div>
      </div>
    ),
    size,
  );
}
