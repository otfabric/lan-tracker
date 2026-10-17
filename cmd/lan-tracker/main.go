// Command lan-tracker is the LAN Tracker daemon, CLI client, database
// inspector and on-demand scanner in one binary.
package main

import (
	"context"
	"os"

	"github.com/otfabric/lan-tracker/internal/cli"
)

func main() {
	os.Exit(cli.Execute(context.Background(), cli.OSEnv()))
}
