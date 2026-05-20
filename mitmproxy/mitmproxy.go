package mitmproxy

import (
	"strconv"
	"strings"
)

const DefaultImage = "localhost/inspect-networking-mitmproxy-runner:latest"

type Config struct {
	NoProxy   bool
	PCAP      bool
	SaveFlows bool
	BodyLimit int
	Port      string
}

func RuntimeFiles(Config) map[string]string {
	return map[string]string{
		"Dockerfile":      dockerfile,
		"entrypoint.sh":   entrypointScript,
		"mitm_capture.py": captureScript,
	}
}

func PodmanArgs(cfg Config) []string {
	if !cfg.PCAP {
		return nil
	}
	return []string{"--cap-add=NET_RAW"}
}

func Environment(cfg Config) []string {
	return []string{
		envPair("INSPECT_USE_PROXY", boolString(!cfg.NoProxy)),
		envPair("INSPECT_CAPTURE_PCAP", boolString(cfg.PCAP)),
		envPair("INSPECT_SAVE_FLOWS", boolString(cfg.SaveFlows)),
		envPair("INSPECT_BODY_LIMIT", strconv.Itoa(cfg.BodyLimit)),
		envPair("INSPECT_MITM_PORT", cfg.Port),
	}
}

func envPair(name, value string) string {
	return name + "=" + value
}

func boolString(v bool) string {
	if v {
		return "1"
	}
	return "0"
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
        python3 \
        python3-venv \
        tcpdump \
    && rm -rf /var/lib/apt/lists/*

RUN python3 -m venv /opt/mitmproxy \
    && /opt/mitmproxy/bin/pip install --no-cache-dir --upgrade pip \
    && /opt/mitmproxy/bin/pip install --no-cache-dir mitmproxy

ENV PATH="/opt/mitmproxy/bin:${PATH}"
WORKDIR /workspace
`) + "\n"

var entrypointScript = strings.TrimSpace(`
#!/usr/bin/env bash
set -euo pipefail

OUT_DIR="${INSPECT_OUT_DIR:-/out}"
MITM_PORT="${INSPECT_MITM_PORT:-18080}"
USE_PROXY="${INSPECT_USE_PROXY:-1}"
CAPTURE_PCAP="${INSPECT_CAPTURE_PCAP:-0}"
SAVE_FLOWS="${INSPECT_SAVE_FLOWS:-0}"

mkdir -p "$OUT_DIR"
: > "$OUT_DIR/events.jsonl"

cleanup() {
    local status=$?
    if [[ -n "${TCPDUMP_PID:-}" ]]; then
        kill "$TCPDUMP_PID" >/dev/null 2>&1 || true
        wait "$TCPDUMP_PID" >/dev/null 2>&1 || true
    fi
    if [[ -n "${MITM_PID:-}" ]]; then
        kill "$MITM_PID" >/dev/null 2>&1 || true
        wait "$MITM_PID" >/dev/null 2>&1 || true
    fi
    exit "$status"
}
trap cleanup EXIT

if [[ "$CAPTURE_PCAP" == "1" ]]; then
    if command -v tcpdump >/dev/null 2>&1; then
        tcpdump -i any -U -w "$OUT_DIR/traffic.pcap" >"$OUT_DIR/tcpdump.log" 2>&1 &
        TCPDUMP_PID=$!
    else
        echo "tcpdump not installed" > "$OUT_DIR/tcpdump.log"
    fi
fi

if [[ "$USE_PROXY" == "1" ]]; then
    export INSPECT_EVENTS_PATH="$OUT_DIR/events.jsonl"

    mitm_args=(
        --listen-host 127.0.0.1
        --listen-port "$MITM_PORT"
        --set block_global=false
        --set flow_detail=0
        -s /inspect/mitm_capture.py
    )
    if [[ "$SAVE_FLOWS" == "1" ]]; then
        mitm_args+=(-w "$OUT_DIR/flows.mitm")
    fi

    mitmdump "${mitm_args[@]}" >"$OUT_DIR/mitmproxy.log" 2>&1 &
    MITM_PID=$!

    for _ in $(seq 1 200); do
        if [[ -s "$HOME/.mitmproxy/mitmproxy-ca-cert.pem" ]]; then
            break
        fi
        if ! kill -0 "$MITM_PID" >/dev/null 2>&1; then
            echo "mitmproxy exited before producing a CA; see mitmproxy.log" >&2
            exit 1
        fi
        sleep 0.05
    done

    if [[ ! -s "$HOME/.mitmproxy/mitmproxy-ca-cert.pem" ]]; then
        echo "timed out waiting for mitmproxy CA" >&2
        exit 1
    fi

    cp "$HOME/.mitmproxy/mitmproxy-ca-cert.pem" /usr/local/share/ca-certificates/inspect-networking-mitmproxy.crt
    update-ca-certificates >"$OUT_DIR/update-ca-certificates.log" 2>&1 || true

    proxy_url="http://127.0.0.1:${MITM_PORT}"
    export HTTP_PROXY="$proxy_url"
    export HTTPS_PROXY="$proxy_url"
    export ALL_PROXY="$proxy_url"
    export http_proxy="$proxy_url"
    export https_proxy="$proxy_url"
    export all_proxy="$proxy_url"
    export NO_PROXY="${NO_PROXY:-localhost,127.0.0.1,::1}"
    export no_proxy="${no_proxy:-localhost,127.0.0.1,::1}"
    export SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
    export REQUESTS_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt
    export CURL_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt
    export GIT_SSL_CAINFO=/etc/ssl/certs/ca-certificates.crt
    export NODE_EXTRA_CA_CERTS="$HOME/.mitmproxy/mitmproxy-ca-cert.pem"
fi

export SSLKEYLOGFILE="${SSLKEYLOGFILE:-$OUT_DIR/sslkeylogfile.log}"

if [[ -s /inspect/setup.sh ]]; then
    bash /inspect/setup.sh > >(tee "$OUT_DIR/setup.stdout.log") 2> >(tee "$OUT_DIR/setup.stderr.log" >&2)
fi

if [[ "$#" -eq 0 ]]; then
    echo "missing target command" >&2
    exit 2
fi

printf '%s\n' "$*" > "$OUT_DIR/command.txt"
set +e
"$@" > >(tee "$OUT_DIR/target.stdout.log") 2> >(tee "$OUT_DIR/target.stderr.log" >&2)
status=$?
set -e
printf '%s\n' "$status" > "$OUT_DIR/exit-code.txt"
exit "$status"
`) + "\n"

var captureScript = strings.TrimSpace(`
from mitmproxy import http

import base64
import hashlib
import json
import os
import threading
import time


EVENTS_PATH = os.environ.get("INSPECT_EVENTS_PATH", "/out/events.jsonl")
BODY_LIMIT = int(os.environ.get("INSPECT_BODY_LIMIT", "0") or "0")
_lock = threading.Lock()


def write_event(event):
    event["ts"] = time.time()
    with _lock:
        with open(EVENTS_PATH, "a", encoding="utf-8") as f:
            f.write(json.dumps(event, sort_keys=True, separators=(",", ":")) + "\n")


SENSITIVE_HEADERS = {
    "authorization",
    "proxy-authorization",
    "cookie",
    "set-cookie",
    "x-api-key",
    "api-key",
}


def header_pairs(headers):
    pairs = []
    for name, value in headers.items(multi=True):
        if name.lower() in SENSITIVE_HEADERS:
            value = "<redacted>"
        pairs.append([name, value])
    return pairs



def body_info(content):
    raw = content or b""
    info = {
        "body_size": len(raw),
        "body_sha256": hashlib.sha256(raw).hexdigest() if raw else "",
    }
    if BODY_LIMIT > 0 and raw:
        chunk = raw[:BODY_LIMIT]
        info["body_base64"] = base64.b64encode(chunk).decode("ascii")
        info["body_truncated"] = len(raw) > BODY_LIMIT
    return info


def conn_addr(conn):
    addr = getattr(conn, "address", None)
    if addr is None:
        return None
    return list(addr)


class Capture:
    def request(self, flow: http.HTTPFlow):
        req = flow.request
        event = {
            "type": "http.request",
            "flow_id": flow.id,
            "scheme": req.scheme,
            "method": req.method,
            "host": req.host,
            "port": req.port,
            "path": req.path,
            "url": req.pretty_url,
            "http_version": req.http_version,
            "headers": header_pairs(req.headers),
            "client": conn_addr(flow.client_conn),
            "server": conn_addr(flow.server_conn),
        }
        event.update(body_info(req.raw_content))
        write_event(event)

    def response(self, flow: http.HTTPFlow):
        req = flow.request
        resp = flow.response
        event = {
            "type": "http.response",
            "flow_id": flow.id,
            "scheme": req.scheme,
            "method": req.method,
            "host": req.host,
            "port": req.port,
            "path": req.path,
            "url": req.pretty_url,
            "status_code": resp.status_code,
            "reason": resp.reason,
            "http_version": resp.http_version,
            "headers": header_pairs(resp.headers),
        }
        event.update(body_info(resp.raw_content))
        write_event(event)

    def error(self, flow: http.HTTPFlow):
        write_event({
            "type": "http.error",
            "flow_id": flow.id,
            "url": flow.request.pretty_url if flow.request else "",
            "error": str(flow.error) if flow.error else "",
        })


addons = [Capture()]
`) + "\n"
