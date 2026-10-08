"use client";
import { useState } from "react";

export function AdminButton({ body, label, ask, reason }: { body: Record<string, unknown>; label: string; ask?: string; reason?: boolean }) {
  const [msg, setMsg] = useState("");
  return (
    <>
      <button
        className="btn btn-quiet btn-sm"
        onClick={async () => {
          if (ask && !window.confirm(ask)) return;
          const why = reason ? window.prompt("Reason (the customer sees it):", "phishing reported") : undefined;
          if (reason && !why) return;
          const res = await fetch("/api/admin", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ...body, reason: why ?? undefined }) });
          const j = (await res.json().catch(() => ({}))) as { message?: string };
          setMsg(j.message ?? (res.ok ? "Done." : "Failed."));
          if (res.ok) setTimeout(() => window.location.reload(), 1000);
        }}
      >
        {label}
      </button>
      {msg && <span className="cp-hint">{msg}</span>}
    </>
  );
}
