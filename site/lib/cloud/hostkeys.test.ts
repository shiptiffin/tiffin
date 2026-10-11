import { describe, expect, test } from "bun:test";
import { hostFingerprints, knownHosts, recoveryCommand } from "./hostkeys";

// Host keys made with ssh-keygen for these tests (public halves only).
const ED = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINbgit9Y8WPU/QBdrxDAtouyK+piPxWmFbJ3gKgMj4Po";
const ED_FP = "SHA256:7oJ928ukdFrY45owr81fVRyRDiHgQDndpc0H7aU96zY";
const P521 =
  "ecdsa-sha2-nistp521 AAAAE2VjZHNhLXNoYTItbmlzdHA1MjEAAAAIbmlzdHA1MjEAAACFBAC13vfeIoyNM1VSm/zmA6HpFDnWgcjf+mO62c9jpsEeb1MLtV2DhkYIZ/pidRcc1ItJAcGl1o83XwuyJHKOq61VmwESScZQojT60BaksRNXs4ttyTa2lUFgYV9MblOoLUquD9qgI/b5virAdjxCTpFi/ISfveHGa3R+bX4C8h3kNrL+qQ==";
const P521_FP = "SHA256:4LRLS0OMavLQ0YnzRU5Lj86qYK1Na8Vd07KaBZ5kxdA";

describe("the server's identity, recorded during setup", () => {
  test("known_hosts lines: well-formed ones, once each", () => {
    const text = `acme.shiptiffin.app ${ED}\n203.0.113.5  ${ED}\n\nacme.shiptiffin.app ${P521}\nacme.shiptiffin.app ${ED}\ngarbage line\n@cert-authority * ${ED}\n`;
    expect(knownHosts(text)).toEqual([`acme.shiptiffin.app ${ED}`, `203.0.113.5 ${ED}`, `acme.shiptiffin.app ${P521}`]);
    expect(knownHosts(null)).toEqual([]);
  });

  test("each host key's fingerprint once, as ssh-keygen -l prints it", () => {
    const lines = knownHosts(`acme.shiptiffin.app ${ED}\n203.0.113.5 ${ED}\nacme.shiptiffin.app ${P521}`);
    expect(hostFingerprints(lines)).toEqual([
      { kind: "ED25519", fingerprint: ED_FP },
      { kind: "ECDSA P-521", fingerprint: P521_FP },
    ]);
  });

  test("the recovery line", () => {
    expect(recoveryCommand("acme")).toBe("ssh root@acme.shiptiffin.app tiffin login");
  });
});
