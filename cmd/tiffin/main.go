// Command tiffin is the single Tiffin binary: CLI, box server and MCP server.
package main

import (
	"context"
	"os"

	"github.com/btahir/tiffin/internal/cli"
)

func main() {
	os.Exit(cli.Execute(context.Background(), os.Args[1:], cli.IO{Out: os.Stdout, Err: os.Stderr}))
}
