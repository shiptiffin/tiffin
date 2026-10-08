// SPDX-License-Identifier: AGPL-3.0-only

// Command tiffin is the single Tiffin binary: CLI, box server and MCP server.
// It is free software under the GNU Affero General Public License v3.0 only;
// see LICENSE, and README (Licensing) for the parts that are Apache-2.0.
package main

import (
	"context"
	"os"

	"github.com/btahir/tiffin/internal/cli"
)

func main() {
	os.Exit(cli.Execute(context.Background(), os.Args[1:], cli.IO{Out: os.Stdout, Err: os.Stderr}))
}
