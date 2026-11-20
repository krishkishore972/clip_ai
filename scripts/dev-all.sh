#!/usr/bin/env bash

set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
api_dir="$repo_root/apps/api"

if [[ ! -s "$api_dir/.env" ]]; then
	printf 'apps/api/.env is missing or empty. Run "make env" in apps/api first.\n' >&2
	exit 1
fi

pids=()

cleanup() {
	local pid
	for pid in "${pids[@]}"; do
		kill "$pid" 2>/dev/null || true
	done
	for pid in "${pids[@]}"; do
		wait "$pid" 2>/dev/null || true
	done
}

trap cleanup EXIT INT TERM

start() {
	local name="$1"
	shift
	printf '[dev-all] starting %s\n' "$name"
	"$@" &
	pids+=("$!")
}

start api bash -c "cd '$api_dir' && make run"
start dispatcher bash -c "cd '$api_dir' && make dispatcher"
start media-worker bash -c "cd '$api_dir' && make media-worker"

printf '[dev-all] all services started; press Ctrl-C to stop them\n'
wait
