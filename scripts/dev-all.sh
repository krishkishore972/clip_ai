#!/usr/bin/env bash

set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
api_dir="$repo_root/apps/api"
llm_dir="$repo_root/apps/llm"

if [[ ! -s "$api_dir/.env" ]]; then
	printf 'apps/api/.env is missing or empty. Run "make env" in apps/api first.\n' >&2
	exit 1
fi

if [[ ! -s "$llm_dir/.env" ]]; then
	printf 'apps/llm/.env is missing or empty. Configure its service token and Groq key first.\n' >&2
	exit 1
fi

for command in make pnpm setsid; do
	if ! command -v "$command" >/dev/null 2>&1; then
		printf '[dev-all] required command not found: %s\n' "$command" >&2
		exit 1
	fi
done

pids=()

cleanup() {
	local pid
	for pid in "${pids[@]}"; do
		# Signal the entire service group, including make/go run descendants.
		kill -TERM -- "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
	done
	for pid in "${pids[@]}"; do
		wait "$pid" 2>/dev/null || true
	done
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

start() {
	local name="$1"
	shift
	printf '[dev-all] starting %s\n' "$name"
	setsid "$@" &
	pids+=("$!")
}

printf '[dev-all] existing .env flags are unchanged; enabled workers may make paid calls\n'

start llm bash -c 'cd "$1" && exec pnpm exec node --env-file=.env --import tsx src/index.ts' dev-all "$llm_dir"
start api bash -c 'cd "$1" && exec make run' dev-all "$api_dir"
start media-worker bash -c 'cd "$1" && exec make media-worker' dev-all "$api_dir"
start transcription-worker bash -c 'cd "$1" && exec make transcription-worker' dev-all "$api_dir"
start curation-worker bash -c 'cd "$1" && exec make curation-worker' dev-all "$api_dir"
start dispatcher bash -c 'cd "$1" && exec make dispatcher' dev-all "$api_dir"

printf '[dev-all] processes launched (not a readiness check); press Ctrl-C to stop them\n'
wait
