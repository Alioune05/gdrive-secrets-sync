package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseGlobalDefaults(t *testing.T) {
	t.Setenv("GDRIVE_SECRETS_SYNC_CREDENTIALS", "/tmp/creds.json")
	t.Setenv("GDRIVE_SECRETS_SYNC_TOKEN", "/tmp/tok.json")

	opts, cmd, rest, err := ParseGlobal([]string{"status"})
	if err != nil {
		t.Fatalf("ParseGlobal: %v", err)
	}
	if cmd != "status" || len(rest) != 0 {
		t.Fatalf("cmd=%q rest=%v", cmd, rest)
	}
	if opts.CredentialsPath != "/tmp/creds.json" || opts.TokenPath != "/tmp/tok.json" {
		t.Fatalf("env defaults not applied: %+v", opts)
	}
}

func TestParseGlobalFlags(t *testing.T) {
	opts, cmd, rest, err := ParseGlobal([]string{
		"--config", "/c.yaml",
		"--credentials", "/cred.json",
		"--token", "/t.json",
		"--groups", "alpha", "beta",
		"-y",
		"push", "--delete",
	})
	if err != nil {
		t.Fatalf("ParseGlobal: %v", err)
	}
	if cmd != "push" {
		t.Fatalf("cmd = %q", cmd)
	}
	if !reflect.DeepEqual(rest, []string{"--delete"}) {
		t.Fatalf("rest = %v", rest)
	}
	want := Options{
		ConfigPath:      "/c.yaml",
		CredentialsPath: "/cred.json",
		TokenPath:       "/t.json",
		Groups:          []string{"alpha", "beta"},
		Yes:             true,
	}
	if !reflect.DeepEqual(opts, want) {
		t.Fatalf("opts = %+v, want %+v", opts, want)
	}
}

func TestParseGlobalHelp(t *testing.T) {
	_, _, _, err := ParseGlobal([]string{"-h"})
	if !errors.Is(err, ErrHelp) {
		t.Fatalf("want ErrHelp, got %v", err)
	}
	for _, f := range []string{"--h", "--help"} {
		if _, _, _, err := ParseGlobal([]string{f}); !errors.Is(err, ErrHelp) {
			t.Errorf("%s: want ErrHelp, got %v", f, err)
		}
	}
}

func TestParseGlobalErrors(t *testing.T) {
	cases := map[string][]string{
		"no command":         {},
		"missing config":     {"--config"},
		"missing cred":       {"--credentials"},
		"missing token":      {"--token"},
		"unknown flag":       {"--nope"},
		"unknown before cmd": {"-z", "status"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := ParseGlobal(args); err == nil {
				t.Fatalf("expected error for %v", args)
			}
		})
	}
}

func TestParsePushArgs(t *testing.T) {
	if d, err := parsePushArgs(nil); err != nil || d {
		t.Fatalf("empty: d=%v err=%v", d, err)
	}
	if d, err := parsePushArgs([]string{"--delete"}); err != nil || !d {
		t.Fatalf("--delete: d=%v err=%v", d, err)
	}
	if _, err := parsePushArgs([]string{"--bogus"}); err == nil {
		t.Fatal("expected error for unknown push arg")
	}
}

func TestWantsHelp(t *testing.T) {
	if !wantsHelp([]string{"foo", "--help"}) {
		t.Error("should detect --help")
	}
	if wantsHelp([]string{"foo", "bar"}) {
		t.Error("should not detect help")
	}
}

func TestResolveConfigPathExplicit(t *testing.T) {
	got, err := resolveConfigPath("/explicit/path.yaml")
	if err != nil || got != "/explicit/path.yaml" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestResolveConfigPathNotFound(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	_, err := resolveConfigPath("")
	if err == nil || !strings.Contains(err.Error(), "no .gdrive-sync.yaml found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestNewConfirm(t *testing.T) {
	var out bytes.Buffer
	confirm := newConfirm(strings.NewReader("Y\n"), &out)
	if !confirm("Overwrite? ") {
		t.Error("'Y' should confirm")
	}
	if !strings.Contains(out.String(), "Overwrite?") {
		t.Errorf("prompt not written: %q", out.String())
	}

	confirm = newConfirm(strings.NewReader("n\n"), &out)
	if confirm("again? ") {
		t.Error("'n' should decline")
	}

	confirm = newConfirm(strings.NewReader(""), &out)
	if confirm("eof? ") {
		t.Error("EOF should decline")
	}
}

// --- Run --------------------------------------------------------------------

func TestRunHelp(t *testing.T) {
	var out, errb bytes.Buffer
	code := Run(context.Background(), []string{"-h"}, &out, &errb, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.HasPrefix(out.String(), "usage: gdrive-secrets-sync") {
		t.Fatalf("usage not printed:\n%s", out.String())
	}
}

func TestRunHelpAfterCommand(t *testing.T) {
	var out, errb bytes.Buffer
	code := Run(context.Background(), []string{"pull", "-h"}, &out, &errb, strings.NewReader(""))
	if code != 0 || !strings.Contains(out.String(), "usage:") {
		t.Fatalf("code=%d out=%q", code, out.String())
	}
}

func TestRunParseError(t *testing.T) {
	var out, errb bytes.Buffer
	code := Run(context.Background(), []string{"--nope"}, &out, &errb, strings.NewReader(""))
	if code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(errb.String(), "unrecognized argument") {
		t.Fatalf("stderr = %q", errb.String())
	}
}

func TestRunInit(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	var out, errb bytes.Buffer
	code := Run(context.Background(), []string{"init"}, &out, &errb, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".gdrive-sync.yaml")); err != nil {
		t.Fatalf("init did not create config: %v", err)
	}
	if !strings.Contains(out.String(), "Wrote template config") {
		t.Fatalf("stdout = %q", out.String())
	}

	// Running init again must fail (refuses to overwrite).
	out.Reset()
	errb.Reset()
	code = Run(context.Background(), []string{"init"}, &out, &errb, strings.NewReader(""))
	if code != 1 {
		t.Fatalf("second init should fail, code = %d", code)
	}
}

func TestRunInitExplicitTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "custom.yaml")
	var out, errb bytes.Buffer
	code := Run(context.Background(), []string{"init", target}, &out, &errb, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code = %d stderr=%q", code, errb.String())
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("custom target not created: %v", err)
	}
}

func TestRunConfigNotFound(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	var out, errb bytes.Buffer
	// status needs a config; none exists here.
	code := Run(context.Background(), []string{"status"}, &out, &errb, strings.NewReader(""))
	if code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(errb.String(), "no .gdrive-sync.yaml found") {
		t.Fatalf("stderr = %q", errb.String())
	}
}

func TestRunInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, ".gdrive-sync.yaml")
	if err := os.WriteFile(cfgPath, []byte("drive: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := Run(context.Background(), []string{"--config", cfgPath, "status"}, &out, &errb, strings.NewReader(""))
	if code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if errb.Len() == 0 {
		t.Fatal("expected an error message on stderr")
	}
}
