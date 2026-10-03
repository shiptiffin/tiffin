const list = document.getElementById("entries");
const status = document.getElementById("status");
const fmt = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

function render(entries) {
  list.replaceChildren(...entries.map((e) => {
    const li = document.createElement("li");
    const who = document.createElement("strong");
    who.textContent = e.name;
    const when = document.createElement("time");
    when.textContent = fmt.format(new Date(e.created_at));
    const msg = document.createElement("p");
    msg.textContent = e.message;
    li.append(who, when, msg);
    return li;
  }));
  if (!entries.length) list.innerHTML = '<li class="empty">No notes yet. Be the first.</li>';
}

async function load() {
  const r = await fetch("/api/entries");
  const d = await r.json();
  document.getElementById("greeting").textContent = d.greeting + ". Leave a note; it goes into Postgres on this box.";
  document.getElementById("visits").textContent = d.visits.toLocaleString() + " visits counted in Valkey";
  render(d.entries);
}

document.getElementById("form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const data = Object.fromEntries(new FormData(ev.target));
  status.textContent = "Saving…";
  const r = await fetch("/api/entries", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(data) });
  if (r.ok) {
    ev.target.reset();
    status.textContent = "Signed. Thank you.";
    window.tiffin?.track?.("signed");
    load();
  } else {
    status.textContent = (await r.json()).error ?? "Something went wrong.";
  }
});

load();
