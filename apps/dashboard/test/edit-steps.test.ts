import { describe, expect, test } from "bun:test";
import { editSteps, editText, INSTALL_CLI, type EditInput } from "@/lib/edit-steps";

const box = "https://dashboard.shop.shiptiffin.app";
const starter: EditInput = { project: "shop", app: "web", dashboard: box, live: "https://shop.shop.shiptiffin.app", source: { kind: "starter", edit: "app/page.jsx" } };
const titles = (i: EditInput) => editSteps(i).steps.map((s) => s.title);

describe("Edit code's steps", () => {
  test("a starter on a box on a server: key, install, connect, pull, change, deploy, check", () => {
    expect(titles(starter)).toEqual(["Create an API key", "Install the tiffin CLI", "Connect it to this box", "Get the code", "Make your change", "Deploy it", "Check it"]);
    const t = editText(starter);
    expect(t).toContain(`$ ${INSTALL_CLI}`);
    expect(t).toContain(`$ export TIFFIN_URL=${box} TIFFIN_TOKEN=<your key>`);
    expect(t).toContain(`Settings › API keys (${box}/settings/keys)`);
    expect(t).toContain("$ tiffin pull shop --project shop && cd shop");
    expect(t).toContain("web’s home page is app/page.jsx.");
    expect(t).toContain("$ tiffin deploy");
    expect(t).toContain("$ tiffin logs web");
    expect(t).toContain("open https://shop.shop.shiptiffin.app.");
    expect(t).toContain("go through tiffin plan, then tiffin apply");
    expect(t).toContain("keep it out of your code");
  });

  test("a box on this computer needs no key and no install", () => {
    const i = { ...starter, dashboard: "https://dashboard.tiffin.localhost:8443" };
    expect(titles(i)[0]).toBe("Check the CLI reaches this box");
    const t = editText(i);
    expect(t).not.toContain("TIFFIN_TOKEN");
    expect(t).not.toContain("API key");
  });

  test("a GitHub app: clone, change, push; no CLI", () => {
    const i: EditInput = { ...starter, source: { kind: "github", repo: "acme/mono", branch: "main", path: "/apps/web/" } };
    expect(titles(i)).toEqual(["Clone the repository", "Make your change", "Push it"]);
    const t = editText(i);
    expect(t).toContain("$ git clone https://github.com/acme/mono.git && cd mono/apps/web");
    expect(t).toContain("$ git push origin main");
    expect(t).not.toContain("$ tiffin");
  });

  test("an app built from a git URL gets the config beside its clone", () => {
    const i: EditInput = { ...starter, live: undefined, source: { kind: "git", url: "https://gitlab.com/acme/site.git" } };
    const t = editText(i);
    expect(t).toContain("$ git clone https://gitlab.com/acme/site.git site && cd site");
    expect(t).toContain("$ tiffin pull --project shop");
    expect(t).toContain("open the address tiffin deploy printed");
  });
});
