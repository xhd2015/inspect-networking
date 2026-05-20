# inspect-networking

`inspect-networking` runs a command inside a Podman Linux container and records
network activity. It defaults to `mitmproxy` for HTTP/HTTPS metadata and can use
Linux `strace` with `--strace` for syscall-level network tracing.

There is no universal single CLI that can decrypt arbitrary HTTPS from any
binary. Plain HTTP is visible, low-level network events can be captured, but
HTTPS bodies require either a trusted MITM CA or cooperation from the program.
Certificate pinning, custom trust stores, direct sockets that ignore proxy
environment variables, and QUIC/HTTP/3 can still limit visibility.

`strace` is useful for seeing network syscalls from a binary that ignores proxy
settings, but it does not decrypt HTTPS. It records calls such as `socket`,
`connect`, `sendto`, and `recvfrom`; TLS payloads remain encrypted at the socket
boundary.

## Usage

Build the CLI:

```bash
go build -o inspect-networking .
```

Capture a normal command:

```bash
./inspect-networking run -- curl https://example.com
```

Capture syscall-level network events with `strace`:

```bash
./inspect-networking run --strace -- curl https://example.com
```

Install common tools before running the target:

```bash
./inspect-networking run \
  --install claude-code \
  --install opencode \
  --install go \
  -- opencode run
```

Supported `--install` values are `codex`, `claude-code`, `opencode`, and
`go`. The flag is repeatable. Install commands run before any `--setup`
commands.

The wrapper also auto-installs a supported tool when the wrapped command starts
with its binary name. For example, `codex ...`, `opencode ...`, `claude ...`,
and `go ...` automatically add the corresponding installer. In `codex` mode,
`--no-install` disables the automatic Codex install.

Capture Codex CLI install and execution:

```bash
./inspect-networking codex exec "one word of french capacity"
```

In `codex` mode, the wrapper automatically mounts `$CODEX_HOME` when set, or
`~/.codex` when present, at `/root/.codex` in the container. That lets the
container reuse your local Codex login instead of starting unauthenticated. Use
`--no-codex-home` for a clean container.

If you previously built the runner image and still see Codex's bubblewrap
warning, rebuild once:

```bash
./inspect-networking codex --rebuild exec "hello"
```

That shorthand is equivalent to:

```bash
./inspect-networking run \
  --install codex \
  -- codex exec "one word of french capacity"
```

The command writes captures under `inspect-networking-runs/<timestamp>/` by
default:

- `events.jsonl` contains request/response metadata in default mitmproxy mode.
- `mitmproxy.log` contains proxy logs in default mitmproxy mode.
- `strace.log` contains target network syscall traces when `--strace` is used.
- `setup.strace.log` contains setup command syscall traces when `--strace` is
  used.
- `target.stdout.log` and `target.stderr.log` contain target output.
- `setup.stdout.log` and `setup.stderr.log` contain setup command output.
- `traffic.pcap` is written only when `--pcap` is enabled.
- `flows.mitm` is written only when `--save-flows` is enabled.

## Useful Options

```bash
# Include up to 4096 body bytes, base64-encoded, in events.jsonl.
./inspect-networking run --body-limit 4096 -- curl https://example.com

# Save full mitmproxy flows. This can include credentials and request bodies.
./inspect-networking run --save-flows -- curl https://example.com

# Do not mask sensitive header values in events.jsonl.
./inspect-networking run --unmask-token -- opencode run

# Use strace instead of mitmproxy.
./inspect-networking codex --strace exec "one word of french capacity"

# Install tools before the target. Repeat --install as needed.
./inspect-networking run --install opencode --install go -- opencode run

# Also try low-level packet capture inside the container.
./inspect-networking run --pcap -- curl https://example.com

# Pass host env or explicit values into the container.
./inspect-networking run --env OPENAI_API_KEY --env FOO=bar -- command

# Mount an existing Codex config directory into the container.
./inspect-networking codex --codex-home "$HOME/.codex" exec "hello"

# Do not reuse local Codex auth/config.
./inspect-networking codex --no-codex-home exec "hello"
```

## What It Can and Cannot See

The wrapper sets `HTTP_PROXY`, `HTTPS_PROXY`, common CA bundle variables, and
`NODE_EXTRA_CA_CERTS` before running setup and target commands. That is enough
for many CLI tools, including npm, curl, git, Python requests, and Node-based
programs.

If a binary ignores proxy environment variables, the JSON HTTP events will be
empty or incomplete. Use `--strace` to see syscall-level network activity or
`--pcap` to see packet-level traffic. A future transparent-proxy mode using
Linux `iptables`/`nftables` with `CAP_NET_ADMIN` would catch more programs
without relying on proxy environment variables.
