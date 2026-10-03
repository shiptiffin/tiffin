// One stylesheet for every component, injected once (or import `tiffinStyles`
// into your own CSS if your CSP forbids inline styles). Everything is themable
// with CSS variables on .tf-root or any ancestor:
//
//   :root { --tf-accent: #0f766e; --tf-radius: 6px; --tf-font: "Inter Tight", sans-serif; }
//
// Defaults are Tiffin's: warm paper, ink and one brass accent. Dark mode
// follows the system, or set data-tf-theme="dark" | "light" on an ancestor
// (a `.dark` class on <html> works too).

export const tiffinStyles = /* css */ `
.tf-root {
  --tf-paper: oklch(0.982 0.005 84);
  --tf-surface: oklch(0.995 0.003 85);
  --tf-sunk: oklch(0.962 0.007 80);
  --tf-hover: oklch(0.945 0.008 80);
  --tf-rule: oklch(0.892 0.009 78);
  --tf-rule-strong: oklch(0.835 0.012 76);
  --tf-ink: oklch(0.235 0.012 62);
  --tf-ink-2: oklch(0.43 0.014 66);
  --tf-ink-3: oklch(0.575 0.014 70);
  --tf-accent: oklch(0.6 0.105 78);
  --tf-accent-ink: oklch(0.47 0.09 74);
  --tf-accent-wash: oklch(0.6 0.105 78 / 0.13);
  --tf-primary: var(--tf-ink);
  --tf-on-primary: oklch(0.982 0.005 84);
  --tf-danger: oklch(0.52 0.17 30);
  --tf-danger-wash: oklch(0.6 0.17 30 / 0.09);
  --tf-ok: oklch(0.5 0.085 158);
  --tf-ok-wash: oklch(0.53 0.085 158 / 0.1);
  --tf-radius: 10px;
  --tf-card-radius: 16px;
  --tf-font: "Geist", ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif;
  --tf-font-display: "Fraunces", ui-serif, Georgia, "Times New Roman", serif;
  --tf-font-mono: "Commit Mono", ui-monospace, "SF Mono", Menlo, monospace;
  --tf-shadow: 0 1px 0 oklch(1 0 0 / 0.7) inset, 0 28px 56px -28px oklch(0.3 0.04 60 / 0.28), 0 2px 6px -2px oklch(0.3 0.03 60 / 0.08);
  --tf-pop-shadow: 0 16px 40px -12px oklch(0.3 0.04 60 / 0.3), 0 2px 8px -2px oklch(0.3 0.03 60 / 0.12);
  color: var(--tf-ink);
  font-family: var(--tf-font);
  font-size: 14px;
  line-height: 1.45;
  -webkit-font-smoothing: antialiased;
  font-variant-numeric: tabular-nums;
}
.tf-dark-vars, :where(.dark, [data-tf-theme="dark"]) .tf-root, .tf-root[data-tf-theme="dark"] {
  --tf-paper: oklch(0.172 0.007 68);
  --tf-surface: oklch(0.205 0.008 68);
  --tf-sunk: oklch(0.155 0.006 66);
  --tf-hover: oklch(0.24 0.009 68);
  --tf-rule: oklch(0.275 0.009 68);
  --tf-rule-strong: oklch(0.35 0.011 70);
  --tf-ink: oklch(0.935 0.01 82);
  --tf-ink-2: oklch(0.775 0.014 78);
  --tf-ink-3: oklch(0.6 0.014 74);
  --tf-accent: oklch(0.8 0.1 82);
  --tf-accent-ink: oklch(0.84 0.09 84);
  --tf-accent-wash: oklch(0.8 0.1 82 / 0.12);
  --tf-primary: oklch(0.86 0.075 84);
  --tf-on-primary: oklch(0.172 0.007 68);
  --tf-danger: oklch(0.74 0.15 28);
  --tf-danger-wash: oklch(0.7 0.17 25 / 0.12);
  --tf-ok: oklch(0.77 0.075 158);
  --tf-ok-wash: oklch(0.77 0.075 158 / 0.1);
  --tf-shadow: 0 1px 0 oklch(1 0 0 / 0.04) inset, 0 28px 56px -24px oklch(0 0 0 / 0.7), 0 2px 8px -2px oklch(0 0 0 / 0.4);
  --tf-pop-shadow: 0 16px 40px -12px oklch(0 0 0 / 0.7), 0 2px 8px -2px oklch(0 0 0 / 0.5);
}
@media (prefers-color-scheme: dark) {
  :where(:not([data-tf-theme="light"]):not(.light)) > .tf-root:not([data-tf-theme="light"]),
  :root:not(.light):not([data-tf-theme="light"]) .tf-root:not([data-tf-theme="light"]) {
    --tf-paper: oklch(0.172 0.007 68);
    --tf-surface: oklch(0.205 0.008 68);
    --tf-sunk: oklch(0.155 0.006 66);
    --tf-hover: oklch(0.24 0.009 68);
    --tf-rule: oklch(0.275 0.009 68);
    --tf-rule-strong: oklch(0.35 0.011 70);
    --tf-ink: oklch(0.935 0.01 82);
    --tf-ink-2: oklch(0.775 0.014 78);
    --tf-ink-3: oklch(0.6 0.014 74);
    --tf-accent: oklch(0.8 0.1 82);
    --tf-accent-ink: oklch(0.84 0.09 84);
    --tf-accent-wash: oklch(0.8 0.1 82 / 0.12);
    --tf-primary: oklch(0.86 0.075 84);
    --tf-on-primary: oklch(0.172 0.007 68);
    --tf-danger: oklch(0.74 0.15 28);
    --tf-danger-wash: oklch(0.7 0.17 25 / 0.12);
    --tf-ok: oklch(0.77 0.075 158);
    --tf-ok-wash: oklch(0.77 0.075 158 / 0.1);
    --tf-shadow: 0 1px 0 oklch(1 0 0 / 0.04) inset, 0 28px 56px -24px oklch(0 0 0 / 0.7), 0 2px 8px -2px oklch(0 0 0 / 0.4);
    --tf-pop-shadow: 0 16px 40px -12px oklch(0 0 0 / 0.7), 0 2px 8px -2px oklch(0 0 0 / 0.5);
  }
}
.tf-root *, .tf-root *::before, .tf-root *::after { box-sizing: border-box; }

/* ---- card ---- */
.tf-card {
  width: 100%;
  max-width: 400px;
  background: var(--tf-surface);
  border: 1px solid var(--tf-rule);
  border-radius: var(--tf-card-radius);
  box-shadow: var(--tf-shadow);
  padding: 32px 32px 28px;
  position: relative;
}
@media (max-width: 440px) { .tf-card { padding: 26px 20px 22px; border-radius: 14px; } }
.tf-head { margin-bottom: 22px; }
.tf-logo { display: block; margin-bottom: 18px; }
.tf-logo img, .tf-logo svg { display: block; height: 32px; width: auto; }
.tf-title {
  font-family: var(--tf-font-display);
  font-size: 26px;
  line-height: 1.15;
  font-weight: 480;
  letter-spacing: -0.015em;
  margin: 0;
  color: var(--tf-ink);
  font-variation-settings: "SOFT" 50, "opsz" 72;
}
.tf-sub { margin: 8px 0 0; color: var(--tf-ink-2); font-size: 14.5px; line-height: 1.5; text-wrap: pretty; }
.tf-sub b { color: var(--tf-ink); font-weight: 560; }
.tf-stack { display: flex; flex-direction: column; gap: 14px; }
.tf-row { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.tf-view { animation: tf-in 180ms cubic-bezier(0.22, 1, 0.36, 1); }
@keyframes tf-in { from { opacity: 0; transform: translateY(4px); } to { opacity: 1; transform: none; } }

/* ---- fields ---- */
.tf-field { display: flex; flex-direction: column; gap: 6px; }
.tf-label { font-size: 13px; font-weight: 560; color: var(--tf-ink-2); letter-spacing: 0.005em; }
.tf-input, .tf-select {
  width: 100%;
  height: 44px;
  padding: 0 13px;
  font: inherit;
  font-size: 15px;
  color: var(--tf-ink);
  background: var(--tf-paper);
  border: 1px solid var(--tf-rule-strong);
  border-radius: var(--tf-radius);
  outline: none;
  transition: border-color 120ms, box-shadow 120ms, background 120ms;
  -webkit-appearance: none;
  appearance: none;
}
.tf-select {
  padding-right: 34px;
  background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='12' viewBox='0 0 24 24' fill='none' stroke='%238a8175' stroke-width='2.2' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='m6 9 6 6 6-6'/%3E%3C/svg%3E");
  background-repeat: no-repeat;
  background-position: right 12px center;
}
.tf-input::placeholder { color: var(--tf-ink-3); opacity: 0.8; }
.tf-input:hover, .tf-select:hover { border-color: var(--tf-ink-3); }
.tf-input:focus-visible, .tf-select:focus-visible {
  border-color: var(--tf-accent);
  background: var(--tf-surface);
  box-shadow: 0 0 0 3px var(--tf-accent-wash);
}
.tf-input[aria-invalid="true"] { border-color: var(--tf-danger); }
.tf-hint { font-size: 12.5px; color: var(--tf-ink-3); }
.tf-pw { position: relative; }
.tf-pw .tf-input { padding-right: 64px; }
.tf-pw-toggle {
  position: absolute; right: 6px; top: 6px; height: 32px; padding: 0 10px;
  font: inherit; font-size: 12.5px; font-weight: 560; color: var(--tf-ink-2);
  background: transparent; border: 0; border-radius: 7px; cursor: pointer;
}
.tf-pw-toggle:hover { background: var(--tf-hover); color: var(--tf-ink); }
.tf-pw-toggle:focus-visible { outline: 2px solid var(--tf-accent); outline-offset: 1px; }

/* one-time code */
.tf-otp { display: grid; grid-template-columns: repeat(6, 1fr); gap: 8px; }
.tf-otp input {
  height: 52px; width: 100%; text-align: center; padding: 0;
  font-family: var(--tf-font-mono); font-size: 22px; font-weight: 500;
  color: var(--tf-ink); background: var(--tf-paper);
  border: 1px solid var(--tf-rule-strong); border-radius: var(--tf-radius); outline: none;
  transition: border-color 120ms, box-shadow 120ms;
  caret-color: var(--tf-accent);
}
.tf-otp input:focus-visible { border-color: var(--tf-accent); box-shadow: 0 0 0 3px var(--tf-accent-wash); background: var(--tf-surface); }
.tf-otp input[data-filled="true"] { border-color: var(--tf-ink-3); }

/* ---- buttons ---- */
.tf-btn {
  position: relative;
  display: inline-flex; align-items: center; justify-content: center; gap: 9px;
  height: 44px; padding: 0 16px; width: 100%;
  font: inherit; font-size: 14.5px; font-weight: 560; letter-spacing: 0.005em;
  border-radius: var(--tf-radius); border: 1px solid transparent;
  cursor: pointer; user-select: none; white-space: nowrap;
  transition: background 120ms, border-color 120ms, color 120ms, transform 80ms, box-shadow 120ms;
}
.tf-btn:focus-visible { outline: none; box-shadow: 0 0 0 3px var(--tf-accent-wash), 0 0 0 1px var(--tf-accent); }
.tf-btn:active:not(:disabled) { transform: translateY(0.5px); }
.tf-btn:disabled { cursor: default; opacity: 0.55; }
.tf-btn[aria-busy="true"] { opacity: 1; cursor: progress; }
.tf-btn-primary { background: var(--tf-primary); color: var(--tf-on-primary); box-shadow: 0 1px 0 oklch(1 0 0 / 0.12) inset, 0 1px 2px oklch(0 0 0 / 0.18); }
.tf-btn-primary:hover:not(:disabled) { background: color-mix(in oklch, var(--tf-primary) 88%, var(--tf-accent)); }
.tf-btn-primary:disabled:not([aria-busy="true"]) { background: var(--tf-sunk); color: var(--tf-ink-3); border-color: var(--tf-rule); box-shadow: none; opacity: 1; }
.tf-btn-quiet { background: var(--tf-surface); color: var(--tf-ink); border-color: var(--tf-rule-strong); }
.tf-btn-quiet:hover:not(:disabled) { background: var(--tf-hover); border-color: var(--tf-ink-3); }
.tf-btn-danger { background: transparent; color: var(--tf-danger); border-color: color-mix(in oklch, var(--tf-danger) 40%, transparent); }
.tf-btn-danger:hover:not(:disabled) { background: var(--tf-danger-wash); }
.tf-btn-sm { height: 34px; font-size: 13px; padding: 0 12px; width: auto; border-radius: 8px; }
.tf-btn svg { width: 18px; height: 18px; flex: none; }
.tf-btn-sm svg { width: 15px; height: 15px; }
.tf-social { display: grid; grid-template-columns: 1fr; gap: 10px; }
.tf-social[data-count="2"] { grid-template-columns: 1fr 1fr; }
.tf-link {
  font: inherit; font-size: 13px; font-weight: 520; color: var(--tf-ink-2);
  background: none; border: 0; padding: 2px 0; cursor: pointer;
  text-decoration: underline; text-decoration-color: var(--tf-rule-strong);
  text-underline-offset: 3px; text-decoration-thickness: 1px;
  border-radius: 3px;
}
.tf-link:hover { color: var(--tf-ink); text-decoration-color: var(--tf-ink-3); }
.tf-link:focus-visible { outline: 2px solid var(--tf-accent); outline-offset: 2px; }
.tf-link:disabled { color: var(--tf-ink-3); cursor: default; text-decoration: none; }
.tf-alt { display: flex; flex-wrap: wrap; justify-content: center; gap: 4px 18px; margin-top: 2px; }
.tf-spinner {
  width: 16px; height: 16px; border-radius: 50%;
  border: 2px solid currentColor; border-right-color: transparent;
  animation: tf-spin 700ms linear infinite; opacity: 0.85;
}
@keyframes tf-spin { to { transform: rotate(360deg); } }

/* ---- divider, alerts, footers ---- */
.tf-or { display: flex; align-items: center; gap: 12px; color: var(--tf-ink-3); font-size: 12px; letter-spacing: 0.06em; text-transform: uppercase; margin: 4px 0; }
.tf-or::before, .tf-or::after { content: ""; flex: 1; height: 1px; background: var(--tf-rule); }
.tf-alert {
  display: flex; gap: 10px; align-items: flex-start;
  padding: 10px 12px; border-radius: 9px;
  background: var(--tf-danger-wash); color: var(--tf-danger);
  font-size: 13.5px; line-height: 1.45;
}
.tf-alert svg { width: 16px; height: 16px; flex: none; margin-top: 1.5px; }
.tf-alert[data-tone="ok"] { background: var(--tf-ok-wash); color: var(--tf-ok); }
.tf-alert[data-tone="info"] { background: var(--tf-sunk); color: var(--tf-ink-2); }
.tf-foot { margin-top: 18px; text-align: center; font-size: 13.5px; color: var(--tf-ink-2); }
.tf-foot .tf-link { font-size: 13.5px; color: var(--tf-ink); font-weight: 560; }
.tf-fine { margin-top: 16px; font-size: 12px; color: var(--tf-ink-3); text-align: center; }

/* check your inbox */
.tf-badge-icon {
  width: 48px; height: 48px; border-radius: 14px; margin-bottom: 18px;
  display: grid; place-items: center;
  color: var(--tf-accent-ink); background: var(--tf-accent-wash);
  border: 1px solid color-mix(in oklch, var(--tf-accent) 22%, transparent);
}
.tf-badge-icon svg { width: 24px; height: 24px; }

/* ---- avatar, menus ---- */
.tf-avatar {
  --tf-size: 32px;
  width: var(--tf-size); height: var(--tf-size); flex: none;
  border-radius: 999px; overflow: hidden;
  display: grid; place-items: center;
  font-size: calc(var(--tf-size) * 0.4); font-weight: 600; letter-spacing: 0.01em;
  color: var(--tf-accent-ink); background: var(--tf-accent-wash);
  box-shadow: 0 0 0 1px color-mix(in oklch, var(--tf-accent) 25%, transparent) inset;
  user-select: none;
}
.tf-avatar img { width: 100%; height: 100%; object-fit: cover; }
.tf-avatar[data-shape="square"] { border-radius: 8px; }
.tf-trigger {
  display: inline-flex; align-items: center; gap: 9px;
  font: inherit; color: var(--tf-ink); background: transparent;
  border: 1px solid transparent; border-radius: 999px; padding: 2px; cursor: pointer;
  transition: background 120ms, border-color 120ms;
}
.tf-trigger:hover { background: var(--tf-hover); }
.tf-trigger:focus-visible { outline: none; box-shadow: 0 0 0 3px var(--tf-accent-wash), 0 0 0 1px var(--tf-accent); }
.tf-trigger[data-variant="org"] { border-radius: 10px; padding: 5px 9px 5px 5px; border-color: var(--tf-rule); background: var(--tf-surface); min-width: 0; max-width: 260px; }
.tf-trigger[data-variant="org"]:hover { background: var(--tf-hover); border-color: var(--tf-rule-strong); }
.tf-trigger-text { display: flex; flex-direction: column; align-items: flex-start; min-width: 0; line-height: 1.2; }
.tf-trigger-text b { font-weight: 560; font-size: 13.5px; max-width: 170px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tf-trigger-text span { font-size: 11.5px; color: var(--tf-ink-3); }
.tf-chev { width: 14px; height: 14px; color: var(--tf-ink-3); flex: none; }
.tf-pop-anchor { position: relative; display: inline-block; }
.tf-menu {
  position: absolute; z-index: 50; top: calc(100% + 8px); right: 0;
  min-width: 260px; max-width: min(320px, calc(100vw - 24px));
  background: var(--tf-surface); border: 1px solid var(--tf-rule); border-radius: 12px;
  box-shadow: var(--tf-pop-shadow); padding: 6px;
  animation: tf-pop 140ms cubic-bezier(0.22, 1, 0.36, 1);
  transform-origin: top right;
}
.tf-menu[data-align="start"] { left: 0; right: auto; transform-origin: top left; }
@keyframes tf-pop { from { opacity: 0; transform: translateY(-4px) scale(0.98); } to { opacity: 1; transform: none; } }
.tf-menu-head { display: flex; gap: 11px; align-items: center; padding: 10px 10px 12px; }
.tf-menu-head .tf-who { min-width: 0; }
.tf-who b { display: block; font-weight: 580; font-size: 14px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tf-who .tf-chip { display: inline-flex; vertical-align: 1px; margin-left: 4px; }
.tf-who span:not(.tf-chip) { display: block; font-size: 12.5px; color: var(--tf-ink-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tf-menu-sep { height: 1px; background: var(--tf-rule); margin: 4px 2px; }
.tf-menu-label { padding: 8px 10px 4px; font-size: 11.5px; letter-spacing: 0.05em; text-transform: uppercase; color: var(--tf-ink-3); }
.tf-item {
  display: flex; align-items: center; gap: 10px; width: 100%;
  min-height: 36px; padding: 6px 10px;
  font: inherit; font-size: 13.5px; color: var(--tf-ink); text-align: left;
  background: transparent; border: 0; border-radius: 8px; cursor: pointer;
}
.tf-item svg { width: 16px; height: 16px; color: var(--tf-ink-3); flex: none; }
.tf-item:hover, .tf-item:focus-visible { background: var(--tf-hover); outline: none; }
.tf-item:focus-visible { box-shadow: 0 0 0 1px var(--tf-accent) inset; }
.tf-item[aria-checked="true"] .tf-check { color: var(--tf-accent-ink); }
.tf-item .tf-grow { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tf-item .tf-meta { font-size: 11.5px; color: var(--tf-ink-3); }
.tf-item[data-danger="true"] { color: var(--tf-danger); }
.tf-item[data-danger="true"] svg { color: currentColor; }
.tf-inline-form { display: flex; gap: 6px; padding: 6px 4px 4px; }
.tf-inline-form .tf-input { height: 34px; font-size: 13.5px; border-radius: 8px; padding: 0 10px; }

/* ---- role chips, lists ---- */
.tf-chip {
  display: inline-flex; align-items: center; height: 20px; padding: 0 7px;
  border-radius: 999px; font-size: 11.5px; font-weight: 560; letter-spacing: 0.01em;
  color: var(--tf-ink-2); background: var(--tf-sunk); border: 1px solid var(--tf-rule);
  text-transform: capitalize; white-space: nowrap;
}
.tf-chip[data-role="owner"], .tf-chip[data-role="admin"] { color: var(--tf-accent-ink); background: var(--tf-accent-wash); border-color: color-mix(in oklch, var(--tf-accent) 25%, transparent); }
.tf-section { padding-top: 18px; margin-top: 18px; border-top: 1px solid var(--tf-rule); }
.tf-section-title { font-size: 13px; font-weight: 600; margin: 0 0 10px; color: var(--tf-ink); }
.tf-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; }
.tf-list li { display: flex; align-items: center; gap: 10px; min-height: 40px; padding: 4px 0; border-bottom: 1px solid var(--tf-rule); font-size: 13.5px; }
.tf-list li:last-child { border-bottom: 0; }
.tf-list .tf-grow { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tf-list .tf-meta { font-size: 12px; color: var(--tf-ink-3); }
.tf-empty { font-size: 13px; color: var(--tf-ink-3); padding: 6px 0; }
.tf-copy {
  display: flex; align-items: center; gap: 8px;
  height: 40px; padding: 0 6px 0 12px;
  background: var(--tf-sunk); border: 1px solid var(--tf-rule); border-radius: var(--tf-radius);
  font-family: var(--tf-font-mono); font-size: 12.5px; color: var(--tf-ink-2);
}
.tf-copy .tf-btn { font-family: var(--tf-font); }
.tf-copy code { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tf-seg { display: grid; grid-auto-flow: column; grid-auto-columns: 1fr; padding: 3px; gap: 2px; background: var(--tf-sunk); border: 1px solid var(--tf-rule); border-radius: var(--tf-radius); }
.tf-seg button { height: 32px; font: inherit; font-size: 13px; font-weight: 540; color: var(--tf-ink-2); background: transparent; border: 0; border-radius: 7px; cursor: pointer; text-transform: capitalize; }
.tf-seg button[aria-checked="true"] { background: var(--tf-surface); color: var(--tf-ink); box-shadow: 0 1px 2px oklch(0 0 0 / 0.08), 0 0 0 1px var(--tf-rule); }
.tf-seg button:focus-visible { outline: 2px solid var(--tf-accent); outline-offset: 1px; }
.tf-seg button:disabled { opacity: 0.4; cursor: not-allowed; }
.tf-org-mark { display: flex; align-items: center; gap: 12px; padding: 14px; border: 1px solid var(--tf-rule); border-radius: 12px; background: var(--tf-paper); margin-bottom: 18px; }
.tf-sr { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; border: 0; }

@media (prefers-reduced-motion: reduce) {
  .tf-root *, .tf-root *::before, .tf-root *::after { animation-duration: 1ms !important; transition-duration: 1ms !important; }
}
`;

let injected = false;

/** Adds the stylesheet to the document once. Components call it on mount. */
export function injectStyles() {
  if (injected || typeof document === "undefined") return;
  injected = true;
  if (document.querySelector("style[data-tiffin-auth]")) return;
  const el = document.createElement("style");
  el.setAttribute("data-tiffin-auth", "");
  el.textContent = tiffinStyles;
  document.head.prepend(el);
}
