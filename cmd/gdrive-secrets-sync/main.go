// Command gdrive-secrets-sync syncs git-ignored local secret files with a
// zip stored in a Google Drive folder, driven by a per-repo
// .gdrive-sync.yaml config.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aamoussou/gdrive-secrets-sync/internal/config"
	"github.com/aamoussou/gdrive-secrets-sync/internal/drive"
)

const usage = `usage: gdrive-secrets-sync [--config PATH] [--credentials PATH]
                            [--token PATH] [--groups GROUP [GROUP ...]]
                            [-y] [-h] {pull,push,status,init} ...

Sync git-ignored local secret files with a zip stored in a Google Drive
folder, driven by a per-repo .gdrive-sync.yaml config.

commands:
  pull      Download the zip from Drive and extract the selected groups
  push      Zip the selected groups and upload/update the zip on Drive
  status    Show what's present locally vs. on Drive
  init      Scaffold a starter .gdrive-sync.yaml in the current directory

global flags:
  --config PATH         Path to the config file (default: auto-discovered
                         .gdrive-sync.yaml, searching this directory and its
                         parents, like git looks for .git)
  --credentials PATH    Path to OAuth client secret JSON
                         (env: GDRIVE_SECRETS_SYNC_CREDENTIALS)
  --token PATH          Path to cached OAuth token
                         (env: GDRIVE_SECRETS_SYNC_TOKEN)
  --groups GROUP...     Which groups to sync (default: all groups)
  -y, --yes             Don't prompt for confirmation
  -h, --h, --help       Show this help and exit (works before or after a
                         command, e.g. 'gdrive-secrets-sync pull -h')

push flags:
  --delete    After a successful push, delete the local files that were
              uploaded (prompts unless -y/--yes is also passed)
`

type globalFlags struct {
	configPath      string
	credentialsPath string
	tokenPath       string
	groups          []string
	yes             bool
}

func defaultConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "gdrive-secrets-sync")
}

func envOrDefault(env, def string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return def
}

// parseGlobalFlags consumes global flags from the front of args until it
// hits the subcommand token (or runs out of args), matching the underlying
// argparse-with-subparsers behavior this tool used to have in Python: global
// flags must precede the subcommand.
func parseGlobalFlags(args []string) (*globalFlags, string, []string, error) {
	dir := defaultConfigDir()
	g := &globalFlags{
		credentialsPath: envOrDefault("GDRIVE_SECRETS_SYNC_CREDENTIALS", filepath.Join(dir, "credentials.json")),
		tokenPath:       envOrDefault("GDRIVE_SECRETS_SYNC_TOKEN", filepath.Join(dir, "token.json")),
	}

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--config":
			i++
			if i >= len(args) {
				return nil, "", nil, fmt.Errorf("--config requires a value")
			}
			g.configPath = args[i]
		case a == "--credentials":
			i++
			if i >= len(args) {
				return nil, "", nil, fmt.Errorf("--credentials requires a value")
			}
			g.credentialsPath = args[i]
		case a == "--token":
			i++
			if i >= len(args) {
				return nil, "", nil, fmt.Errorf("--token requires a value")
			}
			g.tokenPath = args[i]
		case a == "--groups":
			i++
			for i < len(args) && !strings.HasPrefix(args[i], "-") {
				g.groups = append(g.groups, args[i])
				i++
			}
			i--
		case a == "-y" || a == "--yes":
			g.yes = true
		case isHelpFlag(a):
			fmt.Print(usage)
			os.Exit(0)
		case strings.HasPrefix(a, "-"):
			return nil, "", nil, fmt.Errorf("unrecognized argument: %s", a)
		default:
			// First non-flag token is the subcommand.
			return g, a, args[i+1:], nil
		}
	}
	return nil, "", nil, fmt.Errorf("a command is required: pull, push, status, or init")
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// isHelpFlag reports whether arg is one of the accepted help flags.
func isHelpFlag(arg string) bool {
	return arg == "-h" || arg == "--h" || arg == "--help"
}

// wantsHelp reports whether any of args is a help flag, so subcommands can
// print usage and exit even after the command token (e.g. `pull -h`).
func wantsHelp(args []string) bool {
	for _, a := range args {
		if isHelpFlag(a) {
			return true
		}
	}
	return false
}

func printUsageAndExit() {
	fmt.Print(usage)
	os.Exit(0)
}

func main() {
	g, command, rest, err := parseGlobalFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	if wantsHelp(rest) {
		printUsageAndExit()
	}

	switch command {
	case "init":
		cmdInit(g, rest)
	case "pull":
		cmdPull(g, rest)
	case "push":
		cmdPush(g, rest)
	case "status":
		cmdStatus(g, rest)
	default:
		fatalf("unknown command: %s\n\n%s", command, usage)
	}
}

func loadConfig(g *globalFlags) *config.SyncConfig {
	path := g.configPath
	if path == "" {
		found, err := config.FindConfig("")
		if err != nil {
			fatalf("%v", err)
		}
		path = found
	}
	if path == "" {
		fatalf("No .gdrive-sync.yaml found in this directory or any parent.\n" +
			"Run 'gdrive-secrets-sync init' to create one, or pass --config.")
	}
	cfg, err := config.Load(path)
	if err != nil {
		fatalf("%v", err)
	}
	return cfg
}

func confirm(prompt string) bool {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	return strings.ToLower(strings.TrimSpace(answer)) == "y"
}

func cmdInit(g *globalFlags, args []string) {
	target := filepath.Join(".", ".gdrive-sync.yaml")
	if len(args) > 0 {
		target = args[0]
	}
	if err := config.Scaffold(target); err != nil {
		fatalf("%v", err)
	}
}

func cmdStatus(g *globalFlags, args []string) {
	cfg := loadConfig(g)
	groups := g.groups
	if len(groups) == 0 {
		groups = cfg.GroupNames()
	}
	wanted, err := cfg.ResolveFiles(groups)
	if err != nil {
		fatalf("%v", err)
	}

	fmt.Printf("Config: %s\n", cfg.Path)
	fmt.Println("Local files:")
	for _, rel := range wanted {
		p := filepath.Join(cfg.Root, rel)
		state := "MISSING"
		if _, err := os.Stat(p); err == nil {
			state = "present"
		}
		fmt.Printf("  %s: %s\n", rel, state)
	}

	ctx := context.Background()
	service, err := drive.GetService(ctx, g.credentialsPath, g.tokenPath)
	if err != nil {
		fatalf("%v", err)
	}
	folder, err := drive.RequireFolder(service, cfg.Drive.Folder)
	if err != nil {
		fatalf("%v", err)
	}
	remote, err := drive.FindFileInFolder(service, cfg.Drive.Filename, folder.ID)
	if err != nil {
		fatalf("%v", err)
	}
	if remote != nil {
		fmt.Printf("Drive ('%s'): '%s' present (id=%s, modified=%s, size=%s bytes)\n",
			cfg.Drive.Folder, remote.Name, remote.ID, remote.ModifiedTime, remote.Size)
	} else {
		fmt.Printf("Drive ('%s'): '%s' NOT found\n", cfg.Drive.Folder, cfg.Drive.Filename)
	}
}

func cmdPull(g *globalFlags, args []string) {
	if len(args) > 0 {
		fatalf("pull: unrecognized arguments: %s", strings.Join(args, " "))
	}
	cfg := loadConfig(g)
	groups := g.groups
	if len(groups) == 0 {
		groups = cfg.GroupNames()
	}
	wantedList, err := cfg.ResolveFiles(groups)
	if err != nil {
		fatalf("%v", err)
	}
	wanted := map[string]bool{}
	wantedBasenames := map[string]string{}
	for _, rel := range wantedList {
		wanted[rel] = true
		wantedBasenames[filepath.Base(rel)] = rel
	}

	ctx := context.Background()
	service, err := drive.GetService(ctx, g.credentialsPath, g.tokenPath)
	if err != nil {
		fatalf("%v", err)
	}
	folder, err := drive.RequireFolder(service, cfg.Drive.Folder)
	if err != nil {
		fatalf("%v", err)
	}
	remote, err := drive.FindFileInFolder(service, cfg.Drive.Filename, folder.ID)
	if err != nil {
		fatalf("%v", err)
	}
	if remote == nil {
		fatalf("No file named '%s' found in Drive folder '%s'.", cfg.Drive.Filename, cfg.Drive.Folder)
	}

	fmt.Printf("Found '%s' in Drive folder '%s' (id=%s, size=%s bytes).\n",
		remote.Name, cfg.Drive.Folder, remote.ID, remote.Size)

	tmpDir, err := os.MkdirTemp("", "gdrive-secrets-sync-")
	if err != nil {
		fatalf("%v", err)
	}
	defer os.RemoveAll(tmpDir)

	zipPath := filepath.Join(tmpDir, cfg.Drive.Filename)
	if err := drive.DownloadFile(service, remote.ID, zipPath); err != nil {
		fatalf("%v", err)
	}

	if err := extractZip(zipPath, cfg, wanted, wantedBasenames, g.yes); err != nil {
		fatalf("%v", err)
	}

	fmt.Println("Pull complete.")
}

func cmdPush(g *globalFlags, args []string) {
	delete := false
	for _, a := range args {
		if a == "--delete" {
			delete = true
		} else {
			fatalf("push: unrecognized arguments: %s", a)
		}
	}

	cfg := loadConfig(g)
	groups := g.groups
	if len(groups) == 0 {
		groups = cfg.GroupNames()
	}
	wanted, err := cfg.ResolveFiles(groups)
	if err != nil {
		fatalf("%v", err)
	}

	var existing, missing []string
	for _, rel := range wanted {
		if _, err := os.Stat(filepath.Join(cfg.Root, rel)); err == nil {
			existing = append(existing, rel)
		} else {
			missing = append(missing, rel)
		}
	}
	if len(missing) > 0 {
		fmt.Printf("Note: not found locally, skipping: %s\n", strings.Join(missing, ", "))
	}
	if len(existing) == 0 {
		fatalf("Nothing to upload: none of the target files exist locally.")
	}

	ctx := context.Background()
	service, err := drive.GetService(ctx, g.credentialsPath, g.tokenPath)
	if err != nil {
		fatalf("%v", err)
	}
	folder, err := drive.RequireFolder(service, cfg.Drive.Folder)
	if err != nil {
		fatalf("%v", err)
	}

	tmpDir, err := os.MkdirTemp("", "gdrive-secrets-sync-")
	if err != nil {
		fatalf("%v", err)
	}
	defer os.RemoveAll(tmpDir)

	zipPath := filepath.Join(tmpDir, cfg.Drive.Filename)
	if err := createZip(zipPath, cfg.Root, existing); err != nil {
		fatalf("%v", err)
	}

	remote, err := drive.FindFileInFolder(service, cfg.Drive.Filename, folder.ID)
	if err != nil {
		fatalf("%v", err)
	}
	fileID, action, err := drive.UploadOrUpdate(service, zipPath, cfg.Drive.Filename, folder.ID, remote)
	if err != nil {
		fatalf("%v", err)
	}

	verb := "Uploaded new"
	if action == "updated" {
		verb = "Updated existing"
	}
	fmt.Printf("%s '%s' in Drive folder '%s' (id=%s) — %s\n",
		verb, cfg.Drive.Filename, cfg.Drive.Folder, fileID, time.Now().Format("2006-01-02T15:04:05"))
	fmt.Println("Push complete.")

	if delete {
		if !g.yes {
			if !confirm(fmt.Sprintf("Delete %d local file(s) that were just pushed? [y/N] ", len(existing))) {
				fmt.Println("Not deleting local files.")
				return
			}
		}
		for _, rel := range existing {
			p := filepath.Join(cfg.Root, rel)
			if err := os.Remove(p); err != nil {
				fatalf("%v", err)
			}
			fmt.Printf("Deleted %s\n", p)
		}
		fmt.Println("Local cleanup complete.")
	}
}
