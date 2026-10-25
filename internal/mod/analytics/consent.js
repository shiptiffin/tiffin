// Tiffin's first-visit prompt, for projects that ask visitors in the EU, EEA
// and UK before remembering their first visit. Add it to your pages:
// <script defer src="/_tiffin/consent.js"></script>
// It shows only to visitors who need asking and haven't answered.
// tiffinConsent.open() shows it again (for a "Change my choice" link).
(() => {
  const P = "/_tiffin/consent";
  let box;
  const choose = (choice) =>
    fetch(P, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ choice }), credentials: "same-origin" })
      .then(() => box && box.remove())
      .catch(() => {});
  const open = () => {
    if (box) box.remove();
    box = document.createElement("div");
    box.setAttribute("role", "dialog");
    box.setAttribute("aria-label", "Remember where you came from?");
    box.style.cssText =
      "position:fixed;left:16px;right:16px;bottom:16px;max-width:30rem;margin:0 auto;z-index:2147483647;padding:14px 16px;border-radius:10px;" +
      "font:14px/1.45 system-ui,sans-serif;color:#1d1d1f;background:#fff;box-shadow:0 6px 30px rgba(0,0,0,.18);border:1px solid rgba(0,0,0,.08)";
    if (matchMedia("(prefers-color-scheme: dark)").matches) box.style.color = "#f2f2f2", box.style.background = "#1e1e1e";
    const p = document.createElement("p");
    p.style.margin = "0 0 10px";
    p.textContent =
      "Can we remember which site sent you here? One cookie for 90 days, holding a random code. It's only for our own statistics and never shared.";
    const row = document.createElement("div");
    row.style.cssText = "display:flex;gap:8px;justify-content:flex-end";
    for (const [label, choice] of [["No thanks", "no"], ["That's fine", "yes"]]) {
      const b = document.createElement("button");
      b.type = "button";
      b.textContent = label;
      b.style.cssText = "font:inherit;padding:6px 12px;border-radius:7px;cursor:pointer;border:1px solid currentColor;background:transparent;color:inherit";
      b.onclick = () => choose(choice);
      row.append(b);
    }
    box.append(p, row);
    document.body.append(box);
  };
  window.tiffinConsent = { open, choose };
  fetch(P, { credentials: "same-origin" })
    .then((r) => r.json())
    .then((s) => s.ask && open())
    .catch(() => {});
})();
