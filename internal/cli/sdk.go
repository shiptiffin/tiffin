package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/btahir/tiffin/internal/sdkpkg"
	"github.com/spf13/cobra"
)

func (a *app) sdkCmd() *cobra.Command {
	sdk := &cobra.Command{Use: "sdk", Short: "Add tiffin-sdk to a project (it ships inside tiffin, not on npm)"}
	var react bool
	add := &cobra.Command{
		Use:   "add [dir]",
		Short: "Vendor tiffin-sdk into a project and add it to package.json",
		Long: "Writes vendor/" + sdkpkg.SDK.File() + " and sets \"tiffin-sdk\": \"" + sdkpkg.SDK.Spec() + "\" in package.json, " +
			"so `bun install` / `npm install` get the SDK from the project itself (on your machine and in the box's builds). " +
			"Commit vendor/ with the app. --react adds the sign-in components too (tiffin-sdk/react, @tiffin/react). " +
			"Run it again after updating tiffin to get the SDK that matches the box.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := addSDK(orDefault(first(args), "."), react)
			if errors.Is(err, sdkpkg.ErrNoPackageJSON) {
				return &exitError{ExitInvalid, "no package.json in " + orDefault(first(args), ".") + ": create the app first (bun init, create-next-app...), then run tiffin sdk add"}
			}
			if err != nil {
				return err
			}
			if a.tty() {
				fmt.Fprintf(a.io.Out, "Wrote %s and added %s to package.json. Next: bun install (or npm install), and commit vendor/.\n",
					strings.Join(res.Files, ", "), strings.Join(res.Deps, ", "))
			} else {
				writeJSON(a.io.Out, res)
			}
			return nil
		},
	}
	add.Flags().BoolVar(&react, "react", false, "also add the React sign-in components (@tiffin/react, imported as tiffin-sdk/react)")
	sdk.AddCommand(add)
	return sdk
}

func addSDK(dir string, react bool) (*sdkpkg.Added, error) {
	pkgs := []sdkpkg.Package{sdkpkg.SDK}
	if react {
		pkgs = append(pkgs, sdkpkg.React)
	}
	return sdkpkg.Add(dir, pkgs...)
}
