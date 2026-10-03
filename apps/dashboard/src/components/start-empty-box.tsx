import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { starterLine, starterOrder, startersQuery, starterThumb } from "@/lib/starters";

/**
 * What the Box shows in its project tier while there are no projects: the
 * one sentence, and the four starters a click away (each opens /new with it
 * picked). The carrier around it is the Box's own drawing.
 *
 *   {names.length === 0 ? <EmptyBoxStart /> : …}
 */
export function EmptyBoxStart() {
  const starters = useQuery(startersQuery);
  const list = [...(starters.data ?? [])].sort((a, b) => starterOrder.indexOf(a.id) - starterOrder.indexOf(b.id));
  return (
    <div className="px-5 pt-7 pb-6 max-sm:px-3.5">
      <p className="sentence text-[1.5rem] leading-8 text-ink max-sm:text-[1.3125rem] max-sm:leading-7">Your tiffin is packed. Nothing in it yet.</p>
      <p className="mt-1.5 max-w-[36rem] text-[0.9375rem] leading-[1.375rem] text-ink-2">
        Every part below passed its checks. Pick a starter and it’s live at its own address in under a minute.
      </p>
      <ul className="mt-5 grid grid-cols-2 gap-2.5 md:grid-cols-4">
        {(list.length ? list : starterOrder.map((id) => ({ id, name: "" }))).map((s) => (
          <li key={s.id}>
            <Link
              to="/new"
              search={{ starter: s.id } as never}
              className="group flex h-full flex-col overflow-hidden rounded-[10px] border border-rule-2 bg-paper-raised transition-[border-color,transform] duration-[var(--dur-state)] ease-[var(--ease-out)] hover:border-rule-3 active:scale-[0.985]"
            >
              <span className="block aspect-[16/10] bg-paper-sunk">
                {starterThumb[s.id] && <img src={starterThumb[s.id]} alt="" width={320} height={200} className="size-full object-contain p-1 transition-transform duration-[var(--dur-enter)] group-hover:scale-[1.03]" />}
              </span>
              <span className="border-t border-rule px-3 pt-2 pb-2.5">
                <span className="block text-[0.84375rem] font-[550] text-ink">{s.name || " "}</span>
                <span className="block text-[0.75rem] leading-4 text-ink-3 max-sm:hidden">{starterLine[s.id]}</span>
              </span>
            </Link>
          </li>
        ))}
      </ul>
      <p className="mt-4 text-[0.8125rem] text-ink-3">
        Or{" "}
        <Link to="/new" search={{ starter: "empty" } as never} className="font-[550] text-brass-ink hover:underline hover:underline-offset-4">
          start an empty project
        </Link>
        , or{" "}
        <Link to="/new" search={{ starter: "git" } as never} className="font-[550] text-brass-ink hover:underline hover:underline-offset-4">
          build one from a git URL
        </Link>
        .
      </p>
    </div>
  );
}
