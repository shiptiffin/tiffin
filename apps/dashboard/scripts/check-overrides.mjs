#!/usr/bin/env node
// Static check: a className passed to a ui/* wrapper must not fight the
// wrapper's own classes. cn() only joins strings (no tailwind-merge in
// production), so when a caller's `max-w-md` meets the wrapper's `max-w-xl`
// the winner is whichever rule Tailwind happens to emit later: often the
// wrapper, so the override is silently ignored.
//
// Defaults a caller may override are written zero-specificity, e.g.
// `[:where(&)]:max-w-xl`, or are props (DialogContent size, Button size).
// This script parses every .tsx under src/, evaluates each wrapper's cn(...)
// for the call site's literal props, and asks tailwind-merge which wrapper
// classes the caller's classes would replace. Any such pair is an error.
// Wrappers are every component that merges a className with cn(), not only
// the ones in components/ui.
//
//   bun run check:overrides            # every wrapper; exit 1 on any conflict
//   node scripts/check-overrides.mjs --ui    # only the ui/* wrappers
//   CSS=dist/assets/index-*.css ...    # also say who wins in that build's CSS
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { extendTailwindMerge } from "tailwind-merge";
import { twMergeConfig } from "../src/lib/cn-config.ts";

const tm = extendTailwindMerge(twMergeConfig);
const root = fileURLToPath(new URL("..", import.meta.url));
const SRC = join(root, "src");
const UI = join(SRC, "components/ui");
const ALL = !process.argv.includes("--ui");

const walk = (d, out = []) => {
  for (const f of readdirSync(d)) {
    const p = join(d, f);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (/\.tsx$/.test(f)) out.push(p);
  }
  return out;
};
const parse = (file) => ts.createSourceFile(file, readFileSync(file, "utf8"), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const visit = (node, fn) => { fn(node); ts.forEachChild(node, (n) => visit(n, fn)); };
const isCn = (n) => ts.isCallExpression(n) && ts.isIdentifier(n.expression) && n.expression.text === "cn";

// ---- 1. Wrappers: exported components in ui/* that pass `className` to cn(...).
const wrappers = new Map(); // name -> { file, consts, defaults, args }
for (const file of walk(ALL ? SRC : UI)) {
  const sf = parse(file);
  const consts = {}; // module-level `const x = "..."` / `const x = { a: "..." } as const`
  for (const st of sf.statements) {
    if (!ts.isVariableStatement(st)) continue;
    for (const d of st.declarationList.declarations) {
      let init = d.initializer;
      while (init && (ts.isAsExpression(init) || ts.isSatisfiesExpression?.(init) || ts.isParenthesizedExpression(init))) init = init.expression;
      if (!init || !ts.isIdentifier(d.name)) continue;
      if (ts.isStringLiteralLike(init)) consts[d.name.text] = init.text;
      else if (ts.isObjectLiteralExpression(init)) {
        const obj = {};
        for (const p of init.properties) if (ts.isPropertyAssignment(p) && ts.isStringLiteralLike(p.initializer)) obj[p.name.text ?? p.name.getText(sf)] = p.initializer.text;
        consts[d.name.text] = obj;
      }
    }
  }
  for (const st of sf.statements) {
    if (!ts.isFunctionDeclaration(st) || !st.name || !st.modifiers?.some((m) => m.kind === ts.SyntaxKind.ExportKeyword)) continue;
    const defaults = {};
    const param = st.parameters[0];
    if (param && ts.isObjectBindingPattern(param.name)) {
      for (const el of param.name.elements) {
        const key = (el.propertyName ?? el.name).getText(sf).replace(/"/g, "");
        if (el.initializer && ts.isStringLiteralLike(el.initializer)) defaults[key] = el.initializer.text;
        if (ts.isIdentifier(el.name) && el.name.text !== key) defaults[`@alias:${el.name.text}`] = key;
      }
    }
    let args = null;
    visit(st, (n) => {
      if (args || !isCn(n)) return;
      if (n.arguments.some((a) => ts.isIdentifier(a) && a.text === "className")) args = n.arguments.filter((a) => !(ts.isIdentifier(a) && a.text === "className"));
    });
    if (args) wrappers.set(st.name.text, { file, sf, consts, defaults, args });
  }
}

// Evaluates a wrapper's cn() argument to the class strings it can produce for the given props.
function evaluate(expr, w, props) {
  const prop = (id) => props[id] ?? props[w.defaults[`@alias:${id}`]] ?? w.defaults[id] ?? w.defaults[w.defaults[`@alias:${id}`]];
  const value = (e) => {
    if (ts.isStringLiteralLike(e)) return e.text;
    if (ts.isIdentifier(e)) return prop(e.text) ?? w.consts[e.text];
    return undefined;
  };
  if (ts.isParenthesizedExpression(expr)) return evaluate(expr.expression, w, props);
  if (ts.isStringLiteralLike(expr)) return [expr.text];
  if (ts.isIdentifier(expr)) { const v = w.consts[expr.text]; return typeof v === "string" ? [v] : []; }
  if (ts.isElementAccessExpression(expr) && ts.isIdentifier(expr.expression)) {
    const table = w.consts[expr.expression.text];
    const key = value(expr.argumentExpression);
    if (table && typeof table === "object") return key !== undefined && table[key] ? [table[key]] : [];
  }
  if (ts.isConditionalExpression(expr)) {
    const c = expr.condition;
    if (ts.isBinaryExpression(c) && [ts.SyntaxKind.EqualsEqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsEqualsToken].includes(c.operatorToken.kind)) {
      const l = value(c.left), r = value(c.right);
      if (l !== undefined && r !== undefined) {
        const eq = (l === r) === (c.operatorToken.kind === ts.SyntaxKind.EqualsEqualsEqualsToken);
        return evaluate(eq ? expr.whenTrue : expr.whenFalse, w, props);
      }
    }
    return [...evaluate(expr.whenTrue, w, props), ...evaluate(expr.whenFalse, w, props)];
  }
  if (ts.isBinaryExpression(expr) && expr.operatorToken.kind === ts.SyntaxKind.AmpersandAmpersandToken) return evaluate(expr.right, w, props);
  return [];
}

// ---- 2. Call sites with a literal className (a string, or string literals inside cn(...) / a template).
const literalClasses = (init) => {
  if (!init) return [];
  if (ts.isStringLiteralLike(init)) return [init.text];
  const out = [];
  const exp = ts.isJsxExpression(init) ? init.expression : init;
  if (!exp) return out;
  visit(exp, (n) => {
    if (ts.isStringLiteralLike(n) && !(n.parent && ts.isBinaryExpression(n.parent) && [ts.SyntaxKind.EqualsEqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsEqualsToken].includes(n.parent.operatorToken.kind))) out.push(n.text);
    if (ts.isTemplateExpression(n)) { out.push(n.head.text); for (const s of n.templateSpans) out.push(s.literal.text); }
  });
  return out;
};

let css = null;
if (process.env.CSS) css = readFileSync(process.env.CSS, "utf8");
const cssPos = (c) => {
  const esc = "." + c.replace(/[^a-zA-Z0-9_-]/g, (ch) => "\\" + ch);
  for (const end of ["{", ":", ","]) { const i = css.indexOf(esc + end); if (i >= 0) return i; }
  return -1;
};

const problems = [];
let sites = 0;
for (const file of walk(SRC)) {
  if (!ALL && file.startsWith(UI)) continue;
  const sf = parse(file);
  visit(sf, (n) => {
    if (!ts.isJsxOpeningElement(n) && !ts.isJsxSelfClosingElement(n)) return;
    const name = n.tagName.getText(sf);
    const w = wrappers.get(name);
    if (!w) return;
    const props = {};
    let callerClasses = [];
    for (const a of n.attributes.properties) {
      if (!ts.isJsxAttribute(a)) continue;
      const key = a.name.getText(sf);
      if (key === "className") callerClasses = literalClasses(a.initializer);
      else if (a.initializer && ts.isStringLiteralLike(a.initializer)) props[key] = a.initializer.text;
    }
    const caller = callerClasses.join(" ").split(/\s+/).filter(Boolean);
    if (!caller.length) return;
    sites++;
    const base = w.args.flatMap((a) => evaluate(a, w, props)).join(" ").split(/\s+/).filter(Boolean);
    const baseKept = new Set(tm(base.join(" ")).split(" "));
    const merged = new Set(tm(base.join(" "), caller.join(" ")).split(" "));
    for (const d of base) {
      if (!baseKept.has(d) || merged.has(d)) continue;
      const winner = caller.find((c) => c !== d && tm(d, c) === c) ?? "?";
      const line = sf.getLineAndCharacterOfPosition(n.getStart(sf)).line + 1;
      let verdict = "";
      if (css) verdict = cssPos(d) > cssPos(winner) ? "  [ignored today: wrapper rule is later]" : "  [caller wins today by rule order]";
      problems.push(`${relative(root, file)}:${line} <${name}> "${winner}" fights the wrapper's "${d}"${verdict}`);
    }
  });
}

if (problems.length) {
  console.error(problems.join("\n"));
  console.error(`\ncheck:overrides: ${problems.length} className override(s) fight a ${ALL ? "component's" : "ui/*"} default (${sites} call sites scanned).`);
  console.error("Make the wrapper default zero-specificity ([:where(&)]:…) or a prop, or drop the caller class.");
  process.exit(1);
}
console.log(`check:overrides: ok (${sites} call sites with a literal className, ${wrappers.size} ${ALL ? "components" : "ui/* wrappers"})`);
