// Package cli parses arguments and wires the concrete adapters (drive client,
// zip archiver, stdin/stdout) into the syncer usecase. Argument parsing and
// command selection are split into small pure functions so they can be tested
// without touching Google Drive or the network.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aamoussou/gdrive-secrets-sync/internal/archive"
	"github.com/aamoussou/gdrive-secrets-sync/internal/config"
	"github.com/aamoussou/gdrive-secrets-sync/internal/drive"
	"github.com/aamoussou/gdrive-secrets-sync/internal/syncer"
)

// Usage is the help text printed for -h/--help and on argument errors.
const Usage = `usage: gdrive-secrets-sync [--config PATH] [--credentials PATH]
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

// ErrHelp is returned by ParseGlobal when a help flag appears in the global
// (pre-command) position, so the caller can print usage and exit 0.
var ErrHelp = errors.New("help requested")

// Options holds the parsed global flags.
type Options struct {
	ConfigPath      string
	CredentialsPath string
	TokenPath       string
	Groups          []string
	Yes             bool
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

// ParseGlobal consumes global flags from the front of args until it hits the
// subcommand token, matching the argparse-with-subparsers behavior this tool
// had in Python: global flags must precede the subcommand. It returns the
// options, the command, and the remaining (post-command) args. A help flag in
// the global position yields ErrHelp.
func ParseGlobal(args []string) (Options, string, []string, error) {
	dir := defaultConfigDir()
	opts := Options{
		CredentialsPath: envOrDefault("GDRIVE_SECRETS_SYNC_CREDENTIALS", filepath.Join(dir, "credentials.json")),
		TokenPath:       envOrDefault("GDRIVE_SECRETS_SYNC_TOKEN", filepath.Join(dir, "token.json")),
	}

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--config":
			i++
			if i >= len(args) {
				return opts, "", nil, fmt.Errorf("--config requires a value")
			}
			opts.ConfigPath = args[i]
		case a == "--credentials":
			i++
			if i >= len(args) {
				return opts, "", nil, fmt.Errorf("--credentials requires a value")
			}
			opts.CredentialsPath = args[i]
		case a == "--token":
			i++
			if i >= len(args) {
				return opts, "", nil, fmt.Errorf("--token requires a value")
			}
			opts.TokenPath = args[i]
		case a == "--groups":
			i++
			for i < len(args) && !strings.HasPrefix(args[i], "-") {
				opts.Groups = append(opts.Groups, args[i])
				i++
			}
			i--
		case a == "-y" || a == "--yes":
			opts.Yes = true
		case isHelpFlag(a):
			return opts, "", nil, ErrHelp
		case strings.HasPrefix(a, "-"):
			return opts, "", nil, fmt.Errorf("unrecognized argument: %s", a)
		default:
			return opts, a, args[i+1:], nil
		}
	}
	return opts, "", nil, fmt.Errorf("a command is required: pull, push, status, or init")
}

// parsePushArgs interprets a push subcommand's positional args, returning
// whether --delete was requested.
func parsePushArgs(args []string) (deleteAfter bool, err error) {
	for _, a := range args {
		if a == "--delete" {
			deleteAfter = true
		} else {
			return false, fmt.Errorf("push: unrecognized arguments: %s", a)
		}
	}
	return deleteAfter, nil
}

// resolveConfigPath returns the config path to use: the explicit one if set,
// otherwise the auto-discovered .gdrive-sync.yaml.
func resolveConfigPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	found, err := config.FindConfig("")
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf(
			"no .gdrive-sync.yaml found in this directory or any parent.\n" +
				"Run 'gdrive-secrets-sync init' to create one, or pass --config")
	}
	return found, nil
}

// newConfirm builds a syncer.Confirm that prints the prompt to out and reads a
// y/N answer from in.
func newConfirm(in io.Reader, out io.Writer) syncer.Confirm {
	reader := bufio.NewReader(in)
	return func(prompt string) bool {
		fmt.Fprint(out, prompt)
		answer, _ := reader.ReadString('\n')
		return strings.ToLower(strings.TrimSpace(answer)) == "y"
	}
}

// Run parses args and executes the requested command, returning a process
// exit code. All I/O flows through the provided streams so the entrypoint
// stays a one-liner and higher-level flows remain testable.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	opts, command, rest, err := ParseGlobal(args)
	if errors.Is(err, ErrHelp) {
		fmt.Fprint(stdout, Usage)
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprint(stderr, Usage)
		return 1
	}
	if wantsHelp(rest) {
		fmt.Fprint(stdout, Usage)
		return 0
	}

	if command == "init" {
		return runInit(rest, stdout, stderr)
	}

	cfgPath, err := resolveConfigPath(opts.ConfigPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	client, err := drive.NewClient(ctx, opts.CredentialsPath, opts.TokenPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	sy := syncer.New(client, archive.Zip{}, stdout, newConfirm(stdin, stdout))

	switch command {
	case "pull":
		if len(rest) > 0 {
			fmt.Fprintf(stderr, "pull: unrecognized arguments: %s\n", strings.Join(rest, " "))
			return 1
		}
		err = sy.Pull(ctx, cfg, opts.Groups, opts.Yes)
	case "push":
		deleteAfter, perr := parsePushArgs(rest)
		if perr != nil {
			fmt.Fprintln(stderr, perr)
			return 1
		}
		err = sy.Push(ctx, cfg, opts.Groups, syncer.PushOptions{Delete: deleteAfter, Yes: opts.Yes})
	case "status":
		err = sy.Status(ctx, cfg, opts.Groups)
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n\n%s", command, Usage)
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func runInit(args []string, stdout, stderr io.Writer) int {
	target := filepath.Join(".", ".gdrive-sync.yaml")
	if len(args) > 0 {
		target = args[0]
	}
	if err := config.Scaffold(target); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Wrote template config to %s.\n", target)
	fmt.Fprintln(stdout, "Edit it, then run 'gdrive-secrets-sync status' to sanity-check it.")
	return 0
}
