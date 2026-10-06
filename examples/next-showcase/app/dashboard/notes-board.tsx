"use client";

import { useActionState, useOptimistic, useRef } from "react";
import { addNote, type NoteState } from "../actions";

type Note = { id: number; text: string; pending?: boolean };

export function NotesBoard({ notes }: { notes: Note[] }) {
  const [shown, addShown] = useOptimistic(notes, (list: Note[], text: string) => [
    { id: -Date.now(), text, pending: true },
    ...list,
  ]);
  const formRef = useRef<HTMLFormElement>(null);
  const [state, action, pending] = useActionState(async (prev: NoteState, form: FormData) => {
    addShown(String(form.get("text") ?? ""));
    formRef.current?.reset();
    return addNote(prev, form);
  }, {});
  return (
    <div>
      <form ref={formRef} action={action} className="row" id="note-form">
        <input name="text" maxLength={280} placeholder="Write a note" aria-label="Note" required />
        <button disabled={pending}>Add</button>
      </form>
      {state.error && <p className="muted">{state.error}</p>}
      <ul className="plain" id="notes">
        {shown.map((n) => (
          <li key={n.id} data-pending={n.pending ? "true" : undefined}>
            {n.text}
          </li>
        ))}
      </ul>
    </div>
  );
}
