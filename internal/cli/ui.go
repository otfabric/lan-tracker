package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

func (a *app) uiCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "ui",
		Short: "The web user interface",
	}
	c.AddCommand(&cobra.Command{
		Use:     "url",
		Short:   "Print the URL of the web UI, with its access token",
		Example: "  sudo lan-tracker ui url\n  ssh -N -L 9735:127.0.0.1:9735 user@edge-box   # on your workstation, then open the URL",
		Long: "Prints the URL that opens the web UI (ui.enabled) in a browser. The part after\n" +
			"# is the access token: treat the URL like a password. The UI listens on\n" +
			"loopback only; from another machine, forward its port with SSH first.\n" +
			"Exit 2 when the UI is not enabled.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := onlyFormats(a.g.output, "table", "json"); err != nil {
				return err
			}
			if a.g.offline {
				return failf(ExitUsage, "%s needs the running daemon; it does not work with --offline", cmd.CommandPath())
			}
			u, err := a.client().UI(cmd.Context())
			if err != nil {
				return a.failed(err)
			}
			if a.g.output == "json" {
				return writeJSON(a.output(), u)
			}
			if !u.Enabled {
				return fail(ExitError, errors.New("the web UI is not enabled: set ui.enabled: true in the configuration and restart the daemon"))
			}
			fmt.Fprintln(a.output(), u.URL)
			return nil
		},
	})
	return c
}
