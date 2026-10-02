// Command tiffin is a placeholder entrypoint; the real CLI replaces it.
package main

import (
	"fmt"
	"os"

	"github.com/btahir/tiffin/internal/version"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println(version.String())
		return
	}
	fmt.Fprintln(os.Stderr, "tiffin: pre-alpha; try `tiffin version`")
	os.Exit(1)
}
