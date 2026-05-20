package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseEnvSpecExplicitValue(t *testing.T) {
	name, value, ok, err := parseEnvSpec("FOO=bar=baz")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || name != "FOO" || value != "bar=baz" {
		t.Fatalf("unexpected result: name=%q value=%q ok=%v", name, value, ok)
	}
}

func TestParseEnvSpecHostValue(t *testing.T) {
	t.Setenv("INSPECT_NETWORKING_TEST_ENV", "from-host")

	name, value, ok, err := parseEnvSpec("INSPECT_NETWORKING_TEST_ENV")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || name != "INSPECT_NETWORKING_TEST_ENV" || value != "from-host" {
		t.Fatalf("unexpected result: name=%q value=%q ok=%v", name, value, ok)
	}
}

func TestParseEnvSpecUnsetHostValue(t *testing.T) {
	os.Unsetenv("INSPECT_NETWORKING_TEST_UNSET")

	_, _, ok, err := parseEnvSpec("INSPECT_NETWORKING_TEST_UNSET")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected unset host env to be skipped")
	}
}

func TestValidateMount(t *testing.T) {
	if err := validateMount("/tmp:/out:ro"); err != nil {
		t.Fatal(err)
	}
	if err := validateMount("/tmp"); err == nil {
		t.Fatal("expected invalid mount to fail")
	}
}

func TestParseCommandLeavesTargetFlagsAlone(t *testing.T) {
	opts, targetArgs, handled, err := parseCommand([]string{
		"--setup", "echo setup",
		"--env", "FOO=bar",
		"curl", "-fsS", "https://example.com",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("did not expect help to be handled")
	}
	if len(opts.setupCommands) != 1 || opts.setupCommands[0] != "echo setup" {
		t.Fatalf("unexpected setup commands: %#v", opts.setupCommands)
	}
	if len(opts.env) != 1 || opts.env[0] != "FOO=bar" {
		t.Fatalf("unexpected env: %#v", opts.env)
	}
	if opts.mode != traceModeMitmProxy {
		t.Fatalf("unexpected mode: %s", opts.mode)
	}
	if len(opts.setupCommands) != 1 {
		t.Fatalf("unexpected setup commands after auto install: %#v", opts.setupCommands)
	}
	want := []string{"curl", "-fsS", "https://example.com"}
	if !equalStrings(targetArgs, want) {
		t.Fatalf("target args mismatch: got %#v want %#v", targetArgs, want)
	}
}

func TestParseCodexCommandAddsCodexAndKeepsArgs(t *testing.T) {
	opts, targetArgs, handled, err := parseCommand([]string{
		"exec", "--model", "gpt-5.4", "hello",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("did not expect help to be handled")
	}
	if len(opts.setupCommands) != 1 || opts.setupCommands[0] != installSpecs["codex"].command {
		t.Fatalf("unexpected setup commands: %#v", opts.setupCommands)
	}
	if opts.mode != traceModeMitmProxy {
		t.Fatalf("unexpected mode: %s", opts.mode)
	}
	want := []string{"codex", "exec", "--model", "gpt-5.4", "hello"}
	if !equalStrings(targetArgs, want) {
		t.Fatalf("target args mismatch: got %#v want %#v", targetArgs, want)
	}
}

func TestParseCodexCommandNoInstallSkipsAutomaticCodexInstall(t *testing.T) {
	opts, targetArgs, handled, err := parseCommand([]string{
		"--no-install",
		"exec", "hello",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("did not expect help to be handled")
	}
	if len(opts.setupCommands) != 0 {
		t.Fatalf("unexpected setup commands: %#v", opts.setupCommands)
	}
	want := []string{"codex", "exec", "hello"}
	if !equalStrings(targetArgs, want) {
		t.Fatalf("target args mismatch: got %#v want %#v", targetArgs, want)
	}
}

func TestParseCodexCommandDiscoversCodexHome(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.Mkdir(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", "")
	t.Setenv("HOME", home)

	opts, _, _, err := parseCommand([]string{"--no-install", "exec", "hello"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if opts.codexHome != codexHome {
		t.Fatalf("codex home mismatch: got %q want %q", opts.codexHome, codexHome)
	}
}

func TestParseCodexCommandPrefersCodeXHomeEnv(t *testing.T) {
	home := t.TempDir()
	envCodexHome := filepath.Join(home, "custom-codex")
	if err := os.Mkdir(envCodexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", envCodexHome)
	t.Setenv("HOME", filepath.Join(home, "without-codex"))

	opts, _, _, err := parseCommand([]string{"--no-install", "exec", "hello"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if opts.codexHome != envCodexHome {
		t.Fatalf("codex home mismatch: got %q want %q", opts.codexHome, envCodexHome)
	}
}

func TestParseCodexCommandCanDisableCodexHome(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", "")
	t.Setenv("HOME", home)

	opts, _, _, err := parseCommand([]string{"--no-install", "--no-codex-home", "exec", "hello"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if opts.codexHome != "" {
		t.Fatalf("expected codex home to be disabled, got %q", opts.codexHome)
	}
}

func TestParseStraceCommand(t *testing.T) {
	opts, targetArgs, handled, err := parseCommand([]string{
		"--strace",
		"curl", "-fsS", "https://example.com",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("did not expect help to be handled")
	}
	if opts.mode != traceModeStrace {
		t.Fatalf("unexpected mode: %s", opts.mode)
	}
	if opts.image == "" {
		t.Fatal("expected default strace image")
	}
	want := []string{"curl", "-fsS", "https://example.com"}
	if !equalStrings(targetArgs, want) {
		t.Fatalf("target args mismatch: got %#v want %#v", targetArgs, want)
	}
}

func TestParseStraceRejectsMitmproxyOnlyFlags(t *testing.T) {
	_, _, _, err := parseCommand([]string{"--strace", "--save-flows", "curl", "https://example.com"}, false)
	if err == nil {
		t.Fatal("expected --save-flows with --strace to fail")
	}
}

func TestParseStraceRejectsUnmaskToken(t *testing.T) {
	_, _, _, err := parseCommand([]string{"--strace", "--unmask-token", "curl", "https://example.com"}, false)
	if err == nil {
		t.Fatal("expected --unmask-token with --strace to fail")
	}
}

func TestParseUnmaskToken(t *testing.T) {
	opts, _, _, err := parseCommand([]string{"--unmask-token", "curl", "https://example.com"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !opts.unmaskToken {
		t.Fatal("expected unmaskToken to be true")
	}
}

func TestInstallCommandsExplicitRepeated(t *testing.T) {
	opts, _, _, err := parseCommand([]string{
		"--install", "claude-code",
		"--install", "opencode",
		"--install", "go",
		"echo", "done",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		installSpecs["claude-code"].command,
		installSpecs["opencode"].command,
		installSpecs["go"].command,
	}
	if !equalStrings(opts.setupCommands, want) {
		t.Fatalf("setup commands mismatch: got %#v want %#v", opts.setupCommands, want)
	}
}

func TestInstallCommandsDeduplicateExplicitAndAuto(t *testing.T) {
	opts, _, _, err := parseCommand([]string{
		"--install", "opencode",
		"opencode", "run",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{installSpecs["opencode"].command}
	if !equalStrings(opts.setupCommands, want) {
		t.Fatalf("setup commands mismatch: got %#v want %#v", opts.setupCommands, want)
	}
}

func TestInstallCommandsAutoInstallKnownTarget(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{name: "codex", command: "codex", want: installSpecs["codex"].command},
		{name: "opencode", command: "opencode", want: installSpecs["opencode"].command},
		{name: "claude", command: "claude", want: installSpecs["claude-code"].command},
		{name: "go", command: "go", want: installSpecs["go"].command},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, _, _, err := parseCommand([]string{tt.command, "--version"}, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(opts.setupCommands) != 1 || opts.setupCommands[0] != tt.want {
				t.Fatalf("unexpected setup commands: %#v", opts.setupCommands)
			}
		})
	}
}

func TestInstallCommandsRunBeforeSetup(t *testing.T) {
	opts, _, _, err := parseCommand([]string{
		"--install", "go",
		"--setup", "go env",
		"go", "version",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		installSpecs["go"].command,
		"go env",
	}
	if !equalStrings(opts.setupCommands, want) {
		t.Fatalf("setup commands mismatch: got %#v want %#v", opts.setupCommands, want)
	}
}

func TestInstallCommandsRejectUnsupportedExplicitTool(t *testing.T) {
	_, _, _, err := parseCommand([]string{"--install", "wat", "echo", "done"}, false)
	if err == nil {
		t.Fatal("expected unsupported install tool to fail")
	}
}

func TestShellCommandQuotesArgs(t *testing.T) {
	got := shellCommand([]string{
		"/opt/homebrew/bin/podman",
		"run",
		"--name",
		"inspect-networking-test",
		"-v",
		"/path with spaces:/workspace",
		"image",
		"codex",
		"exec",
		"one word of french capacity",
		"it's quoted",
		"",
	})
	want := "/opt/homebrew/bin/podman run --name inspect-networking-test -v '/path with spaces:/workspace' image codex exec 'one word of french capacity' 'it'\\''s quoted' ''"
	if got != want {
		t.Fatalf("shell command mismatch:\ngot:  %s\nwant: %s", got, want)
	}
}

func equalStrings(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
