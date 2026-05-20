package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/xhd2015/inspect-networking/mitmproxy"
	inspectstrace "github.com/xhd2015/inspect-networking/strace"
	"github.com/xhd2015/less-gen/flags"
)

const (
	defaultPort = "18080"
)

type traceMode string

const (
	traceModeMitmProxy traceMode = "mitmproxy"
	traceModeStrace    traceMode = "strace"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	args, err := flags.HelpFunc("-h,--help", printTopHelp).
		HelpNoExit().
		StopOnFirstArg().
		Parse(args)
	if errors.Is(err, flags.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}

	if len(args) == 0 {
		printTopHelp()
		return nil
	}

	switch args[0] {
	case "run":
		return runCommand(args[1:], false)
	case "codex":
		return runCommand(args[1:], true)
	case "-h", "--help", "help":
		printTopHelp()
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q\n\nRun `inspect-networking --help`.", args[0])
	}
}

type stringList []string

type options struct {
	image       string
	outDir      string
	workdir     string
	podman      string
	hostWorkdir string
	codexHome   string

	rebuild       bool
	skipBuild     bool
	useStrace     bool
	noProxy       bool
	pcap          bool
	saveFlows     bool
	noInstall     bool
	noCodexHome   bool
	bodyLimit     int
	mitmPort      string
	setupCommands stringList
	installTools  stringList
	env           stringList
	mounts        stringList
	mode          traceMode
}

func runCommand(args []string, codexMode bool) error {
	opts, targetArgs, handled, err := parseCommand(args, codexMode)
	if handled || err != nil {
		return err
	}
	return execute(opts, targetArgs)
}

func parseCommand(args []string, codexMode bool) (options, []string, bool, error) {
	opts := options{
		workdir:     "/workspace",
		podman:      "podman",
		hostWorkdir: ".",
		mitmPort:    defaultPort,
	}

	parser := flags.String("--image", &opts.image).
		String("--out", &opts.outDir).
		String("--workdir", &opts.workdir).
		String("--host-workdir", &opts.hostWorkdir).
		String("--podman", &opts.podman).
		String("--mitm-port", &opts.mitmPort).
		Bool("--rebuild", &opts.rebuild).
		Bool("--skip-build", &opts.skipBuild).
		Bool("--strace", &opts.useStrace).
		Bool("--no-proxy", &opts.noProxy).
		Bool("--pcap", &opts.pcap).
		Bool("--save-flows", &opts.saveFlows).
		Int("--body-limit", &opts.bodyLimit).
		StringSlice("--install", &opts.installTools).
		StringSlice("--setup", &opts.setupCommands).
		StringSlice("--env", &opts.env).
		StringSlice("--mount", &opts.mounts)
	if codexMode {
		parser = parser.
			Bool("--no-install", &opts.noInstall).
			Bool("--no-codex-home", &opts.noCodexHome).
			String("--codex-home", &opts.codexHome)
	}

	targetArgs, err := parser.HelpFunc("-h,--help", func() {
		if codexMode {
			printCodexHelp()
		} else {
			printRunHelp()
		}
	}).HelpNoExit().StopOnFirstArg().Parse(args)
	if errors.Is(err, flags.ErrHelp) {
		return opts, nil, true, nil
	}
	if err != nil {
		return opts, nil, false, err
	}

	if opts.useStrace {
		opts.mode = traceModeStrace
		if opts.pcap {
			return opts, nil, false, errors.New("--pcap is only supported in mitmproxy mode")
		}
		if opts.saveFlows {
			return opts, nil, false, errors.New("--save-flows is only supported in mitmproxy mode")
		}
		if opts.bodyLimit != 0 {
			return opts, nil, false, errors.New("--body-limit is only supported in mitmproxy mode")
		}
		if opts.noProxy {
			return opts, nil, false, errors.New("--no-proxy is only supported in mitmproxy mode")
		}
	} else {
		opts.mode = traceModeMitmProxy
	}
	if opts.image == "" {
		opts.image = defaultImageForMode(opts.mode)
	}

	if codexMode {
		if opts.codexHome == "" && !opts.noCodexHome {
			opts.codexHome = discoverCodexHome()
		}
		targetArgs = append([]string{"codex"}, targetArgs...)
	}
	if len(targetArgs) == 0 {
		return opts, nil, false, errors.New("missing target command")
	}
	autoInstall := true
	if codexMode && opts.noInstall {
		autoInstall = false
	}
	installCommands, err := installCommands(opts.installTools, targetArgs, autoInstall)
	if err != nil {
		return opts, nil, false, err
	}
	opts.setupCommands = append(installCommands, opts.setupCommands...)

	return opts, targetArgs, false, nil
}

type installSpec struct {
	name    string
	command string
}

var installSpecs = map[string]installSpec{
	"codex": {
		name:    "codex",
		command: "npm install -g @openai/codex",
	},
	"claude-code": {
		name:    "claude-code",
		command: "npm install -g @anthropic-ai/claude-code",
	},
	"opencode": {
		name:    "opencode",
		command: "npm install -g opencode-ai",
	},
	"go": {
		name:    "go",
		command: "apt-get update && apt-get install -y --no-install-recommends golang-go",
	},
}

var installAliases = map[string]string{
	"@anthropic-ai/claude-code": "claude-code",
	"@openai/codex":             "codex",
	"claude":                    "claude-code",
	"claude-code":               "claude-code",
	"codex":                     "codex",
	"go":                        "go",
	"golang":                    "go",
	"opencode":                  "opencode",
	"opencode-ai":               "opencode",
	"open-code":                 "opencode",
}

func installCommands(explicitTools []string, targetArgs []string, autoInstall bool) ([]string, error) {
	var commands []string
	seen := make(map[string]bool)

	for _, tool := range explicitTools {
		spec, err := lookupInstallSpec(tool)
		if err != nil {
			return nil, err
		}
		if !seen[spec.name] {
			commands = append(commands, spec.command)
			seen[spec.name] = true
		}
	}
	if autoInstall && len(targetArgs) > 0 {
		if spec, ok := autoInstallSpec(targetArgs[0]); ok && !seen[spec.name] {
			commands = append(commands, spec.command)
		}
	}
	return commands, nil
}

func lookupInstallSpec(tool string) (installSpec, error) {
	canonical, ok := installAliases[strings.ToLower(strings.TrimSpace(tool))]
	if !ok {
		return installSpec{}, fmt.Errorf("unsupported install tool %q; supported tools: %s", tool, supportedInstallTools())
	}
	return installSpecs[canonical], nil
}

func autoInstallSpec(command string) (installSpec, bool) {
	base := filepath.Base(command)
	canonical, ok := installAliases[strings.ToLower(base)]
	if !ok {
		return installSpec{}, false
	}
	return installSpecs[canonical], true
}

func supportedInstallTools() string {
	return "claude-code, codex, go, opencode"
}

func discoverCodexHome() string {
	if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" && isDir(codexHome) {
		return codexHome
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	codexHome := filepath.Join(home, ".codex")
	if isDir(codexHome) {
		return codexHome
	}
	return ""
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func defaultImageForMode(mode traceMode) string {
	switch mode {
	case traceModeStrace:
		return inspectstrace.DefaultImage
	default:
		return mitmproxy.DefaultImage
	}
}

func execute(opts options, targetArgs []string) error {
	podmanPath, err := exec.LookPath(opts.podman)
	if err != nil {
		return fmt.Errorf("%s not found in PATH", opts.podman)
	}

	outDir, err := prepareOutputDir(opts.outDir)
	if err != nil {
		return err
	}

	hostWorkdir, err := filepath.Abs(opts.hostWorkdir)
	if err != nil {
		return fmt.Errorf("resolve host workdir: %w", err)
	}
	if err := ensureDir(hostWorkdir); err != nil {
		return err
	}

	runtimeDir, err := os.MkdirTemp("", "inspect-networking-runtime-*")
	if err != nil {
		return fmt.Errorf("create runtime dir: %w", err)
	}
	defer os.RemoveAll(runtimeDir)

	runtimeFiles, modePodmanArgs, modeEnv, err := runtimeForOptions(opts)
	if err != nil {
		return err
	}
	if err := writeRuntimeFiles(runtimeDir, opts.setupCommands, runtimeFiles); err != nil {
		return err
	}

	if !opts.skipBuild {
		imageExists := false
		if !opts.rebuild {
			imageExists = commandSucceeds(podmanPath, "image", "exists", opts.image)
		}
		if opts.rebuild || !imageExists {
			if err := buildImage(podmanPath, opts.image, runtimeDir); err != nil {
				return err
			}
		}
	}

	podmanArgs := []string{
		"run",
		"--rm",
		"--pull=missing",
		"--name", containerName(),
	}
	podmanArgs = append(podmanArgs, modePodmanArgs...)
	podmanArgs = append(podmanArgs,
		"-v", hostWorkdir+":/workspace",
		"-v", outDir+":/out",
		"-v", runtimeDir+":/inspect:ro",
		"--workdir", opts.workdir,
	)

	for _, env := range modeEnv {
		podmanArgs = append(podmanArgs, "-e", env)
	}
	for _, name := range defaultForwardedEnv() {
		if _, ok := os.LookupEnv(name); ok {
			podmanArgs = append(podmanArgs, "-e", name)
		}
	}
	for _, spec := range opts.env {
		name, value, ok, err := parseEnvSpec(spec)
		if err != nil {
			return err
		}
		if ok {
			if strings.Contains(spec, "=") {
				podmanArgs = append(podmanArgs, "-e", envPair(name, value))
			} else {
				podmanArgs = append(podmanArgs, "-e", name)
			}
		}
	}
	for _, mount := range opts.mounts {
		if err := validateMount(mount); err != nil {
			return err
		}
		podmanArgs = append(podmanArgs, "-v", mount)
	}
	if opts.codexHome != "" {
		codexHome, err := filepath.Abs(opts.codexHome)
		if err != nil {
			return fmt.Errorf("resolve codex home: %w", err)
		}
		if err := ensureDir(codexHome); err != nil {
			return err
		}
		podmanArgs = append(podmanArgs, "-v", codexHome+":/root/.codex")
	}

	podmanArgs = append(podmanArgs, opts.image, "/inspect/entrypoint.sh")
	podmanArgs = append(podmanArgs, targetArgs...)

	fmt.Fprintf(os.Stderr, "capture output: %s\n", outDir)
	if opts.codexHome != "" {
		fmt.Fprintf(os.Stderr, "codex home: %s\n", opts.codexHome)
	}
	if err := runPodman(podmanPath, podmanArgs); err != nil {
		return err
	}
	return nil
}

func containerName() string {
	return fmt.Sprintf("inspect-networking-%d-%d", os.Getpid(), time.Now().UnixNano())
}

func runPodman(podmanPath string, args []string) error {
	name := podmanContainerName(args)
	printCommand("podman command", podmanPath, args)
	cmd := exec.Command(podmanPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return err
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- cmd.Wait()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case err := <-errCh:
		return err
	case sig := <-sigCh:
		fmt.Fprintf(os.Stderr, "\nreceived %s; stopping container\n", sig)
		if name != "" {
			stopContainer(podmanPath, name)
		}
		if cmd.Process != nil {
			_ = cmd.Process.Signal(sig)
		}
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-errCh
		}
		return fmt.Errorf("interrupted")
	}
}

func podmanContainerName(args []string) string {
	for i, arg := range args {
		if arg == "--name" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func stopContainer(podmanPath, name string) {
	cmd := exec.Command(podmanPath, "stop", "--time", "2", name)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

func printCommand(label, name string, args []string) {
	argv := append([]string{name}, args...)
	fmt.Fprintf(os.Stderr, "%s: %s\n", label, shellCommand(argv))
}

func shellCommand(argv []string) string {
	quoted := make([]string, 0, len(argv))
	for _, arg := range argv {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	if isShellSafe(arg) {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
}

func isShellSafe(arg string) bool {
	for _, r := range arg {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case strings.ContainsRune("@%_+=:,./-", r):
		default:
			return false
		}
	}
	return true
}

func runtimeForOptions(opts options) (map[string]string, []string, []string, error) {
	switch opts.mode {
	case traceModeMitmProxy:
		cfg := mitmproxy.Config{
			NoProxy:   opts.noProxy,
			PCAP:      opts.pcap,
			SaveFlows: opts.saveFlows,
			BodyLimit: opts.bodyLimit,
			Port:      opts.mitmPort,
		}
		return mitmproxy.RuntimeFiles(cfg), mitmproxy.PodmanArgs(cfg), mitmproxy.Environment(cfg), nil
	case traceModeStrace:
		cfg := inspectstrace.Config{}
		return inspectstrace.RuntimeFiles(cfg), inspectstrace.PodmanArgs(cfg), inspectstrace.Environment(cfg), nil
	default:
		return nil, nil, nil, fmt.Errorf("unknown trace mode %q", opts.mode)
	}
}

func prepareOutputDir(path string) (string, error) {
	if path == "" {
		path = filepath.Join("inspect-networking-runs", time.Now().Format("20060102-150405"))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve output dir: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}
	return abs, nil
}

func ensureDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}

func writeRuntimeFiles(dir string, setupCommands []string, runtimeFiles map[string]string) error {
	files := map[string]string{
		"setup.sh":          setupScript(setupCommands),
		"setup-summary.txt": strings.Join(setupCommands, "\n"),
	}
	for name, content := range runtimeFiles {
		files[name] = content
	}

	for name, content := range files {
		path := filepath.Join(dir, name)
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}

func setupScript(commands []string) string {
	if len(commands) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\nset -euo pipefail\n")
	for _, command := range commands {
		b.WriteString(command)
		b.WriteByte('\n')
	}
	return b.String()
}

func buildImage(podmanPath, image, runtimeDir string) error {
	fmt.Fprintf(os.Stderr, "building runner image %s\n", image)
	args := []string{"build", "-t", image, "-f", filepath.Join(runtimeDir, "Dockerfile"), runtimeDir}
	printCommand("podman build command", podmanPath, args)
	cmd := exec.Command(podmanPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build runner image: %w", err)
	}
	return nil
}

func commandSucceeds(name string, args ...string) bool {
	cmd := exec.Command(name, args...)
	return cmd.Run() == nil
}

func parseEnvSpec(spec string) (string, string, bool, error) {
	if spec == "" {
		return "", "", false, errors.New("empty --env")
	}
	if strings.Contains(spec, "=") {
		name, value, _ := strings.Cut(spec, "=")
		if name == "" {
			return "", "", false, fmt.Errorf("invalid --env %q", spec)
		}
		return name, value, true, nil
	}
	value, ok := os.LookupEnv(spec)
	if !ok {
		fmt.Fprintf(os.Stderr, "warning: host env %s is not set; skipping\n", spec)
		return spec, "", false, nil
	}
	return spec, value, true, nil
}

func validateMount(mount string) error {
	parts := strings.Split(mount, ":")
	if len(parts) < 2 {
		return fmt.Errorf("invalid --mount %q, expected SRC:DST[:MODE]", mount)
	}
	if parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid --mount %q, expected SRC:DST[:MODE]", mount)
	}
	return nil
}

func envPair(name, value string) string {
	return name + "=" + value
}

func defaultForwardedEnv() []string {
	return []string{
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		"OPENAI_ORGANIZATION",
		"OPENAI_ORG_ID",
		"OPENAI_PROJECT",
	}
}

func printTopHelp() {
	fmt.Print(strings.TrimSpace(`
inspect-networking runs a command in a Podman Linux container and traces network
activity. It defaults to mitmproxy; use --strace for Linux network syscall
tracing instead.

Usage:
  inspect-networking run [options] -- <command> [args...]
  inspect-networking codex [options] [codex args...]

Examples:
  inspect-networking run -- curl https://example.com
  inspect-networking run --strace -- curl https://example.com
  inspect-networking run --install codex -- codex exec "one word of french capacity"
  inspect-networking codex exec "one word of french capacity"

Run inspect-networking run --help or inspect-networking codex --help for options.
`))
	fmt.Println()
}

func printRunHelp() {
	fmt.Print(strings.TrimSpace(`
Usage:
  inspect-networking run [options] -- <command> [args...]

Options:
  --setup CMD       Run a shell setup command before the target. Repeatable.
  --install TOOL    Install a known tool before the target. Repeatable.
  --env NAME[=VAL]  Pass an environment variable into the container. Repeatable.
  --mount SPEC      Add a podman volume mount SRC:DST[:MODE]. Repeatable.
  --out DIR         Write capture output to DIR.
  --strace          Use Linux strace network syscall tracing instead of mitmproxy.
  --image IMAGE     Runner image tag.
  --host-workdir D  Host directory mounted at /workspace.
  --workdir D       Container working directory.
  --rebuild         Rebuild the runner image.
  --skip-build      Do not auto-build the runner image.

Mitmproxy options:
  --pcap            Also attempt tcpdump capture to traffic.pcap.
  --body-limit N    Include up to N body bytes in JSONL events.
  --save-flows      Save full mitmproxy flows, including bodies.
  --no-proxy        Disable mitmproxy.
`))
	fmt.Println()
}

func printCodexHelp() {
	fmt.Print(strings.TrimSpace(`
Usage:
  inspect-networking codex [options] [codex args...]

This is shorthand for:
  inspect-networking run --install codex -- codex ...

Options:
  --no-install      Skip automatic Codex CLI install.
  --codex-home DIR  Mount host DIR at /root/.codex. Defaults to $CODEX_HOME or ~/.codex.
  --no-codex-home   Do not auto-mount $CODEX_HOME or ~/.codex.
  --install TOOL    Install a known tool before Codex. Repeatable.
  --env NAME[=VAL]  Pass an environment variable into the container. Repeatable.
  --out DIR         Write capture output to DIR.
  --strace          Use Linux strace network syscall tracing instead of mitmproxy.
  --rebuild         Rebuild the runner image.
  --skip-build      Do not auto-build the runner image.

Mitmproxy options:
  --pcap            Also attempt tcpdump capture to traffic.pcap.
  --body-limit N    Include up to N body bytes in JSONL events.
  --save-flows      Save full mitmproxy flows, including bodies.
  --no-proxy        Disable mitmproxy.
`))
	fmt.Println()
}
