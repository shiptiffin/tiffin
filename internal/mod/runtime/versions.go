package runtime

// Pinned upstream releases, in one place. Every download is verified against
// these sha256 sums before it is unpacked. To upgrade: change the version,
// update both sums from the release's own checksum file, run the e2e test.
//
//	nerdctl-full 2.4.1 bundles containerd 2.4.1, runc 1.5.2, BuildKit 0.33.1,
//	CNI plugins and tini (all Apache-2.0, tini MIT).
//	Railpack 0.40.1 (MIT) plans builds; its BuildKit frontend image runs them.
//	Bun 1.4.2 (MIT) builds static sites; Railpack installs Bun inside app images.
const (
	NerdctlVersion  = "2.4.1"
	RailpackVersion = "0.40.1"
	BunVersion      = "1.4.2"
)

// nerdctlFullSHA256 is per GOARCH (nerdctl-full-<v>-linux-<arch>.tar.gz).
var nerdctlFullSHA256 = map[string]string{
	"amd64": "ff018f11ac2f1354bf7b3aaaca6e0b52d81139c746a89134744d0b7be3a0e62f",
	"arm64": "2deffe14625f93866f90a8f265d0f59d6fa476c7dc54b98adb327b1b4613efd4",
}

// railpackSHA256 is per GOARCH (railpack-v<v>-<arch>-unknown-linux-musl.tar.gz).
var railpackSHA256 = map[string]string{
	"amd64": "2842de93e68713af9037e0bc0a398d7da78f3b96aa4804303a638db2bc69bd30",
	"arm64": "c24a064b586b8f4f8c2fab44dd5ef19253e4c6cc4e1df793b3ae19cd87f7a5d4",
}

// Images pulled by builds. Pinned by digest (multi-arch index).
const (
	// RailpackFrontend is the BuildKit frontend matching RailpackVersion.
	RailpackFrontend = "ghcr.io/railwayapp/railpack-frontend:v" + RailpackVersion + "@sha256:f1973377693af30c9b37a92c97c661c07b277ccdc6be909213c74c771f8d2d6d"
	// BunImage runs `bun install && bun run build` for static sites.
	BunImage = "docker.io/oven/bun:" + BunVersion + "-slim@sha256:cb3bbbb08e13a4a2ff400f24c7a2a1d5efa83f6ef8544d52d95a519631e2fc61"
)

// WorkflowPostgresWorld is the Workflow DevKit's Postgres world (Apache-2.0)
// the box adds to apps that use the DevKit, by the major version of the
// app's `workflow` package, which it must match.
var WorkflowPostgresWorld = map[string]string{"4": "4.3.9", "5": "5.0.1"}

func nerdctlURL(arch string) string {
	return "https://github.com/containerd/nerdctl/releases/download/v" + NerdctlVersion +
		"/nerdctl-full-" + NerdctlVersion + "-linux-" + arch + ".tar.gz"
}

func railpackURL(arch string) string {
	a := map[string]string{"amd64": "x86_64", "arm64": "arm64"}[arch]
	return "https://github.com/railwayapp/railpack/releases/download/v" + RailpackVersion +
		"/railpack-v" + RailpackVersion + "-" + a + "-unknown-linux-musl.tar.gz"
}
