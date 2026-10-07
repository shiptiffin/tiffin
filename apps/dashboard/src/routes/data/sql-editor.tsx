// The SQL editor: CodeMirror 6 with Postgres highlighting and completion of
// table and column names. Loaded only when the SQL tab opens.
import { autocompletion, closeBrackets, closeBracketsKeymap, closeCompletion, completionKeymap } from "@codemirror/autocomplete";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { PostgreSQL, sql, type SQLNamespace } from "@codemirror/lang-sql";
import { bracketMatching, HighlightStyle, indentOnInput, syntaxHighlighting } from "@codemirror/language";
import { Compartment, EditorState, Prec } from "@codemirror/state";
import { drawSelection, EditorView, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers, placeholder } from "@codemirror/view";
import { tags as t } from "@lezer/highlight";
import { useEffect, useEffectEvent, useLayoutEffect, useRef } from "react";

const highlight = HighlightStyle.define([
  { tag: [t.keyword, t.operatorKeyword, t.modifier], color: "var(--graphite)", fontWeight: "550" },
  { tag: [t.string, t.special(t.string)], color: "var(--brass-ink)" },
  { tag: [t.number, t.bool, t.null], color: "var(--ink-2)" },
  { tag: [t.lineComment, t.blockComment], color: "var(--ink-3)", fontStyle: "italic" },
  { tag: [t.typeName, t.standard(t.name)], color: "var(--ink-2)" },
  { tag: [t.name, t.propertyName, t.special(t.name)], color: "var(--ink)" },
  { tag: [t.punctuation, t.operator], color: "var(--ink-3)" },
]);

const theme = EditorView.theme({
  "&": { color: "var(--ink)", backgroundColor: "transparent", fontSize: "0.8125rem", height: "100%" },
  "&.cm-focused": { outline: "none" },
  ".cm-scroller": { fontFamily: "var(--mono)", lineHeight: "1.5rem", overflow: "auto" },
  ".cm-content": { padding: "0.75rem 0", caretColor: "var(--ink)" },
  ".cm-line": { padding: "0 0.875rem" },
  ".cm-gutters": { backgroundColor: "color-mix(in oklch, var(--paper-sunk) 60%, transparent)", color: "var(--ink-3)", border: "none", borderRight: "1px solid var(--rule)" },
  ".cm-lineNumbers .cm-gutterElement": { padding: "0 0.625rem 0 0.75rem", minWidth: "2.5rem", fontVariantNumeric: "tabular-nums" },
  ".cm-activeLine": { backgroundColor: "color-mix(in oklch, var(--paper-sunk) 55%, transparent)" },
  ".cm-activeLineGutter": { backgroundColor: "transparent", color: "var(--ink-2)" },
  ".cm-selectionBackground, &.cm-focused .cm-selectionBackground, ::selection": { backgroundColor: "var(--brass-wash) !important" },
  ".cm-cursor": { borderLeftColor: "var(--ink)" },
  ".cm-matchingBracket": { backgroundColor: "var(--brass-wash)", outline: "none" },
  ".cm-placeholder": { color: "var(--ink-3)" },
  ".cm-tooltip": { border: "1px solid var(--rule-2)", backgroundColor: "var(--paper-raised)", borderRadius: "8px", boxShadow: "var(--lift-raised)", overflow: "hidden" },
  ".cm-tooltip-autocomplete > ul": { fontFamily: "var(--mono)", fontSize: "0.78125rem", maxHeight: "16rem" },
  ".cm-tooltip-autocomplete > ul > li": { padding: "0.25rem 0.625rem", color: "var(--ink-2)" },
  ".cm-tooltip-autocomplete > ul > li[aria-selected]": { backgroundColor: "var(--paper-sunk)", color: "var(--ink)" },
  ".cm-completionDetail": { color: "var(--ink-3)", fontStyle: "normal", marginLeft: "0.75rem" },
  ".cm-completionIcon": { display: "none" },
});

/** Tables and their columns, as the completer wants them: { public: { books: ["id", …] } }. */
export type SqlSchema = SQLNamespace;

export default function SqlEditor({
  value,
  onChange,
  onRun,
  schema,
  label = "SQL",
  wrap,
}: {
  value: string;
  onChange: (v: string) => void;
  onRun: () => void;
  schema: SqlSchema;
  label?: string;
  wrap?: boolean;
}) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const lang = useRef(new Compartment());
  const run = useRef(onRun);
  const change = useRef(onChange);
  useLayoutEffect(() => {
    run.current = onRun;
    change.current = onChange;
  });

  // The editor is made once; its first doc, schema, wrap and label are read then (later schema changes reconfigure below).
  const create = useEffectEvent(
    (parent: HTMLElement) =>
      new EditorView({
      parent,
      state: EditorState.create({
        doc: value,
        extensions: [
          Prec.highest(keymap.of([{ key: "Mod-Enter", run: (v) => (closeCompletion(v), run.current(), true) }])),
          lineNumbers(),
          highlightActiveLineGutter(),
          history(),
          drawSelection(),
          indentOnInput(),
          bracketMatching(),
          closeBrackets(),
          autocompletion({ activateOnTyping: true, icons: false }),
          highlightActiveLine(),
          placeholder("SELECT * FROM …"),
          keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...historyKeymap, ...completionKeymap, indentWithTab]),
          lang.current.of(sql({ dialect: PostgreSQL, schema, defaultSchema: "public" })),
          syntaxHighlighting(highlight),
          theme,
          wrap ? EditorView.lineWrapping : [],
          EditorView.contentAttributes.of({ "aria-label": label, "aria-multiline": "true", spellcheck: "false", autocapitalize: "off", autocorrect: "off" }),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) change.current(u.state.doc.toString());
          }),
        ],
      }),
    }),
  );
  useEffect(() => {
    const v = create(host.current!);
    view.current = v;
    return () => v.destroy();
  }, []);

  // New tables: new completions.
  useEffect(() => {
    view.current?.dispatch({ effects: lang.current.reconfigure(sql({ dialect: PostgreSQL, schema, defaultSchema: "public" })) });
  }, [schema]);

  // A value from outside (a saved query, history): replace the text.
  useEffect(() => {
    const v = view.current;
    if (v && v.state.doc.toString() !== value) v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } });
  }, [value]);

  return <div ref={host} className="h-full min-h-0" />;
}
