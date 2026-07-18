// Command gdrive-secrets-sync syncs git-ignored local secret files with a zip
// stored in a Google Drive folder, driven by a per-repo .gdrive-sync.yaml
// config. All logic lives in internal/cli and the packages it wires together;
// this entrypoint just hands off os.Args and the standard streams.
package main

import (
	"context"
	"os"

	"github.com/aamoussou/gdrive-secrets-sync/internal/cli"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, os.Stdin))
}
