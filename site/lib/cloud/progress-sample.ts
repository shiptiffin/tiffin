// A whole setup's steps as the provisioner writes them (internal/cloud/worker.go,
// the Hetzner provider's Ensure, internal/install), with typical gaps between
// them: the tests read it, and so can a local preview of the progress screen.
import type { Step } from "./progress";

const T0 = Date.parse("2026-10-08T12:00:00Z");

const RAW: [number, string][] = [
  [0, "Finding the address this setup connects from"],
  [2, "Checking your Hetzner project"],
  [6, "Creating a cx23 server in Falkenstein (Hetzner bills you about 10.16 USD a month before VAT)"],
  [7, "uploading the SSH public key"],
  [9, "creating the firewall: SSH allowed only from the setup worker's address; HTTP/HTTPS from anywhere"],
  [12, "creating the 40 GB data volume in fsn1"],
  [18, "creating the server acme (cx23 in fsn1)"],
  [38, "attaching the data volume"],
  [44, "Pointing acme.shiptiffin.app at your server"],
  [47, "Waiting for the server to start"],
  [88, "Fetching the signed Tiffin release for amd64"],
  [93, "preparing the data disk at /var/lib/tiffin"],
  [96, "formatting /dev/disk/by-id/scsi-0HC_Volume_101 as XFS"],
  [99, "copying tiffin 3f2a9c1d7e0b to the box"],
  [107, "installing the service"],
  [110, "provisioning system services (first run installs packages; later runs are quick)"],
  [232, "starting tiffin (rolls back automatically if unhealthy)"],
  [244, "Making your one-time sign-in link (it works once, for 24 hours)"],
  [246, "Removing the setup key from your server"],
  [248, "Closing SSH and deleting the setup key from your Hetzner project"],
  [255, "Forgot your Hetzner key (only its fingerprint SHA256:Xk3… stays)"],
  [256, "Waiting for https://dashboard.acme.shiptiffin.app to answer over HTTPS with a valid certificate"],
  [291, "Your box is ready"],
];

export const SAMPLE_START = T0;

/** The first n steps (all of them by default). */
export function sampleSteps(n = RAW.length): Step[] {
  return RAW.slice(0, n).map(([s, text]) => ({ at: new Date(T0 + s * 1000).toISOString().replace(".000", ""), text }));
}

/** The index of the first step whose text starts with `prefix`. */
export function sampleIndex(prefix: string): number {
  return RAW.findIndex(([, t]) => t.startsWith(prefix));
}
