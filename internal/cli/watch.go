package cli

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/shiptiffin/tiffin/internal/watch"
	"github.com/spf13/cobra"
)

// watchCmd runs the outside checker on another machine (hidden: ShipTiffin
// runs it; a box owner can too).
func (a *app) watchCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Watch boxes from outside and alert when one is down",
		Long: "Checks each box's /v1/health, collects the heartbeats boxes send (tiffin monitor set <collector>/ping/<heartbeat>), " +
			"and alerts through a webhook or a command when a box is down for downAfter, and again when it is back. " +
			"Run it on another machine. The config file is JSON:\n\n" +
			"  {\"every\": \"1m\", \"downAfter\": \"5m\", \"listen\": \":8099\",\n" +
			"   \"webhook\": \"https://hooks.slack.com/...\", \"command\": \"mail -s 'tiffin watch' you@example.com\",\n" +
			"   \"boxes\": [{\"name\": \"shop\", \"url\": \"https://dashboard.shop.example.com\", \"heartbeat\": \"<openssl rand -hex 16>\"}]}",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := watch.Load(file)
			if err != nil {
				return &exitError{ExitInvalid, err.Error()}
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return watch.New(cfg, a.io.Err).Run(ctx)
		},
	}
	cmd.Flags().StringVar(&file, "config", "watch.json", "the watch file (JSON)")
	return cmd
}
