// SPDX-License-Identifier: AGPL-3.0-only

// Package tiffin embeds the licence files at the root of the repository so
// the binary carries them: tiffin licenses prints them and the dashboard
// shows them at /licenses.
package tiffin

import (
	_ "embed"
	"strings"
)

var (
	//go:embed LICENSE
	license string
	//go:embed NOTICE
	notice string
	//go:embed THIRD_PARTY_NOTICES
	thirdParty string
)

// Licenses is Tiffin's licence, its NOTICE and the third-party notices
// (scripts/notices.ts), after a header that names the licence and where
// the source of this build is.
func Licenses(version, source string) string {
	var b strings.Builder
	b.WriteString("Tiffin " + version + " is free software under the GNU Affero General Public\n")
	b.WriteString("License v3.0 only (AGPL-3.0-only); the full text is below. The SDK,\n")
	b.WriteString("starters, templates, tracker and build glue that end up inside your apps\n")
	b.WriteString("are Apache-2.0.\n\n")
	b.WriteString("Source code of this version: " + source + "\n")
	for _, part := range []struct{ title, text string }{
		{"THIRD-PARTY NOTICES", thirdParty},
		{"NOTICE", notice},
		{"LICENSE (GNU Affero General Public License v3.0)", license},
	} {
		b.WriteString("\n" + strings.Repeat("#", 78) + "\n# " + part.title + "\n" + strings.Repeat("#", 78) + "\n\n")
		b.WriteString(strings.TrimRight(part.text, "\n") + "\n")
	}
	return b.String()
}
