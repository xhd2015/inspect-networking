package strace

import "strings"

const DefaultImage = "localhost/inspect-networking-strace-runner:latest"

type Config struct{}

func RuntimeFiles(Config) map[string]string {
	return map[string]string{
		"Dockerfile":    dockerfile,
		"entrypoint.sh": entrypointScript,
	}
}

func PodmanArgs(Config) []string {
	return []string{
		"--cap-add=SYS_PTRACE",
		"--security-opt=seccomp=unconfined",
	}
}

func Environment(Config) []string {
	return nil
}

var dockerfile = strings.TrimSpace(`
FROM docker.io/library/node:22-bookworm

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        bubblewrap \
        ca-certificates \
        curl \
        git \
        iproute2 \
        procps \
        strace \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace
`) + "\n"

var entrypointScript = strings.TrimSpace(`
#!/usr/bin/env bash
set -euo pipefail

OUT_DIR="${INSPECT_OUT_DIR:-/out}"
STRACE_STRING_LIMIT="${INSPECT_STRACE_STRING_LIMIT:-4096}"
STRACE_EXPR="${INSPECT_STRACE_EXPR:-trace=network}"

mkdir -p "$OUT_DIR"

run_strace() {
    local log_path="$1"
    shift

    set +e
    strace \
        -f \
        -ttt \
        -T \
        -yy \
        -s "$STRACE_STRING_LIMIT" \
        -e "$STRACE_EXPR" \
        -o "$log_path" \
        "$@"
    local status=$?
    set -e
    return "$status"
}

if [[ -s /inspect/setup.sh ]]; then
    set +e
    run_strace "$OUT_DIR/setup.strace.log" bash /inspect/setup.sh \
        > >(tee "$OUT_DIR/setup.stdout.log") \
        2> >(tee "$OUT_DIR/setup.stderr.log" >&2)
    setup_status=$?
    set -e
    if [[ "$setup_status" -ne 0 ]]; then
        printf '%s\n' "$setup_status" > "$OUT_DIR/exit-code.txt"
        exit "$setup_status"
    fi
fi

if [[ "$#" -eq 0 ]]; then
    echo "missing target command" >&2
    exit 2
fi

printf '%s\n' "$*" > "$OUT_DIR/command.txt"
set +e
run_strace "$OUT_DIR/strace.log" "$@" \
    > >(tee "$OUT_DIR/target.stdout.log") \
    2> >(tee "$OUT_DIR/target.stderr.log" >&2)
status=$?
set -e
printf '%s\n' "$status" > "$OUT_DIR/exit-code.txt"
exit "$status"
`) + "\n"
