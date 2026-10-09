import { onThisComputer } from "@/lib/mcp";

/**
 * Edit code: the steps that get an app's code onto a computer, change it and
 * put it live again, for a person or for the coding agent they paste them
 * into. The dialog shows them and copies them as text; both come from here.
 */

export const INSTALL_CLI = "curl -fsSL https://shiptiffin.com/install.sh | sh";

/** Where an app's live code came from, which decides how to get it. */
export type CodeSource =
  | { kind: "starter"; edit?: string }
  | { kind: "github"; repo: string; branch: string; path?: string }
  | { kind: "git"; url: string; path?: string }
  | { kind: "folder" };

export type Step = {
  title: string;
  note?: string;
  cmds?: string[];
  /** The note names Settings › API keys: the dialog links it. */
  keys?: boolean;
};

export type EditInput = { project: string; app: string; dashboard: string; live?: string; source: CodeSource };

const trimPath = (p?: string) => (p ?? "").trim().replace(/^\/+|\/+$/g, "").replace(/^\.$/, "");
const repoDir = (url: string) => url.replace(/\/+$/, "").replace(/\.git$/, "").split("/").pop() || "code";

function isLocal(dashboard: string) {
  try {
    return onThisComputer(new URL(dashboard).hostname);
  } catch {
    return false;
  }
}

/** Install the CLI and connect it with a key; a box on this computer needs neither. */
function cliSteps(i: EditInput): Step[] {
  if (isLocal(i.dashboard)) return [{ title: "Check the CLI reaches this box", note: "The tiffin CLI on this computer finds the box by itself.", cmds: ["tiffin whoami"] }];
  return [
    { title: "Create an API key", note: `In Settings › API keys, create a key with full access to ${i.project}.`, keys: true },
    { title: "Install the tiffin CLI", note: "macOS or Linux (on Windows, inside WSL).", cmds: [INSTALL_CLI] },
    { title: "Connect it to this box", note: "Use the key from step 1. tiffin whoami then shows its name.", cmds: [`export TIFFIN_URL=${i.dashboard} TIFFIN_TOKEN=<your key>`, "tiffin whoami"] },
  ];
}

/** Change, deploy, check: the end of every way but GitHub's. */
function shipSteps(i: EditInput, edit?: string): Step[] {
  return [
    { title: "Make your change", note: edit ? `${i.app}’s home page is ${edit}.` : "Edit any file, in your editor or with your coding agent." },
    { title: "Deploy it", note: "It builds on the box and switches over once the new version is healthy.", cmds: ["tiffin deploy"] },
    { title: "Check it", note: i.live ? `Read the logs, then open ${i.live}.` : "Read the logs, then open the address tiffin deploy printed.", cmds: [`tiffin logs ${i.app}`] },
  ];
}

export function editSteps(i: EditInput): { lede: string; steps: Step[]; safety: string } {
  const s = i.source;
  const planLine = "Changes to the box itself (services, env vars, domains) go through tiffin plan, then tiffin apply.";
  const safety = isLocal(i.dashboard) ? planLine : `${planLine} The key works like a password: keep it out of your code and anything you share.`;
  switch (s.kind) {
    case "github": {
      const path = trimPath(s.path);
      return {
        lede: `${i.app} deploys from ${s.repo} on GitHub: every push to ${s.branch} goes live.`,
        steps: [
          { title: "Clone the repository", cmds: [`git clone https://github.com/${s.repo}.git && cd ${[repoDir(s.repo), path].filter(Boolean).join("/")}`] },
          { title: "Make your change", note: "Edit any file, in your editor or with your coding agent." },
          {
            title: "Push it",
            note: `A push to ${s.branch} deploys ${i.app}; the build shows on this project’s page. A pull request gets its own preview address.`,
            cmds: [`git add -A && git commit -m "Describe the change"`, `git push origin ${s.branch}`],
          },
        ],
        safety: "Secrets belong in the project’s environment variables on the box, never in the repository.",
      };
    }
    case "git": {
      const path = trimPath(s.path);
      const dir = repoDir(s.url);
      return {
        lede: `${i.app} was built from ${s.url.replace(/^https?:\/\//, "")}. Clone it, add the project’s config and deploy your change with the tiffin CLI.`,
        steps: [
          ...cliSteps(i),
          { title: "Get the code", note: "tiffin pull writes the project’s tiffin.config.ts beside it.", cmds: [`git clone ${s.url} ${dir} && cd ${[dir, path].filter(Boolean).join("/")}`, `tiffin pull --project ${i.project}`] },
          ...shipSteps(i),
        ],
        safety,
      };
    }
    case "starter":
      return {
        lede: `${i.app} runs a starter. Get its code onto your computer, change it and deploy it again, yourself or with your coding agent.`,
        steps: [
          ...cliSteps(i),
          { title: "Get the code", note: `It writes tiffin.config.ts and ${i.app}’s source into a folder called ${i.project}.`, cmds: [`tiffin pull ${i.project} --project ${i.project} && cd ${i.project}`] },
          ...shipSteps(i, s.edit),
        ],
        safety,
      };
    case "folder":
      return {
        lede: `${i.app} was deployed from a folder with the tiffin CLI. Change the code there and deploy it again.`,
        steps: [
          ...cliSteps(i),
          { title: "Open the app’s folder", note: `The one it was deployed from. If it has no tiffin.config.ts, this writes ${i.project}’s.`, cmds: [`tiffin pull --project ${i.project}`] },
          ...shipSteps(i),
        ],
        safety,
      };
  }
}

/** The same steps as plain text, to paste into a coding agent or a note. */
export function editText(i: EditInput): string {
  const { lede, steps, safety } = editSteps(i);
  const keys = `${i.dashboard}/settings/keys`;
  const lines = [`Edit the code of ${i.app}, in the Tiffin project ${i.project} (box: ${i.dashboard}).`, lede, ""];
  steps.forEach((s, n) => {
    lines.push(`${n + 1}. ${s.title}`);
    if (s.note) lines.push(`   ${s.keys ? s.note.replace("Settings › API keys", `Settings › API keys (${keys})`) : s.note}`);
    for (const c of s.cmds ?? []) lines.push(`   $ ${c}`);
  });
  lines.push("", safety);
  return lines.join("\n");
}
