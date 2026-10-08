package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/btahir/tiffin/internal/sdkpkg"
	"github.com/spf13/cobra"
)

func (a *app) sdkCmd() *cobra.Command {
	sdk := &cobra.Command{Use: "sdk", Short: "Add @shiptiffin/sdk to a project without the npm registry"}
	add := &cobra.Command{
		Use:   "add [dir]",
		Short: "Vendor @shiptiffin/sdk into a project and add it to package.json",
		Long: "Writes vendor/" + sdkpkg.SDK.File() + " (the copy built into this tiffin, made for this version of the box; the release on npm may be older or newer) and sets \"" +
			sdkpkg.SDK.Name + "\": \"" + sdkpkg.SDK.Spec() + "\" in package.json, so `bun install` / `npm install` get the SDK " +
			"from the project itself, on your machine and in the box's builds, with no registry. Commit vendor/ with the app. " +
			"An app that already installs " + sdkpkg.SDK.Name + " from npm (bun add " + sdkpkg.SDK.Name + ") is left as it is. " +
			"Run it again after updating tiffin to get the SDK that matches the box.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := sdkpkg.Add(orDefault(first(args), "."))
			if errors.Is(err, sdkpkg.ErrNoPackageJSON) {
				return &exitError{ExitInvalid, "no package.json in " + orDefault(first(args), ".") + ": create the app first (bun init, create-next-app...), then run tiffin sdk add"}
			}
			if err != nil {
				return err
			}
			if !a.tty() {
				writeJSON(a.io.Out, res)
			} else if res.FromNPM != "" {
				fmt.Fprintf(a.io.Out, "%s is already installed from npm (%s): nothing to vendor.\n", sdkpkg.SDK.Name, res.FromNPM)
			} else {
				fmt.Fprintf(a.io.Out, "Wrote %s and added %s to package.json. Next: bun install (or npm install), and commit vendor/.\n",
					strings.Join(res.Files, ", "), strings.Join(res.Deps, ", "))
			}
			return nil
		},
	}
	sdk.AddCommand(add)
	return sdk
}
