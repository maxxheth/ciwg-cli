#!/bin/bash

# Run WordPress/custom app backups in the background with:
# - per-app YAML configs (single file or entire directory)
# - Postgres-friendly defaults via config metadata
# - Minio pruning + capacity guard before each run
# - native --config-dir wiring for batches of YAML files

set -euo pipefail

HOSTNAME="${APP_BACKUP_HOSTNAME:-$(hostname)}"
LOGDIR="${APP_BACKUP_LOG_DIR:-/root/logs}"
LOGFILE="${APP_BACKUP_LOG_FILE:-$LOGDIR/minio-backup.log}"
REMAINDER="${APP_BACKUP_REMAINDER:-4}"
CAPACITY_THRESHOLD="${APP_BACKUP_CAPACITY_THRESHOLD:-80}"
STOP_ON_ERROR="${APP_BACKUP_STOP_ON_ERROR:-false}"

# Sources for YAML configs
CONFIG_DIR_RAW="${APP_BACKUP_CONFIG_DIR:-}"
CONFIG_LIST_RAW="${APP_BACKUP_CONFIGS:-}"
DEFAULT_CONFIG="${APP_BACKUP_DEFAULT_CONFIG:-./api.ciwebgroup.com.yml}"

# Allow a conventional directory without exporting APP_BACKUP_CONFIG_DIR
if [[ -z "$CONFIG_DIR_RAW" && -d ./app-backup-configs ]]; then
	CONFIG_DIR_RAW="./app-backup-configs"
fi

CONFIG_DIR=""
if [[ -n "$CONFIG_DIR_RAW" ]]; then
	if [[ -d "$CONFIG_DIR_RAW" ]]; then
		CONFIG_DIR="$CONFIG_DIR_RAW"
	else
		echo "[WARN] APP_BACKUP_CONFIG_DIR='$CONFIG_DIR_RAW' is not a directory" >&2
	fi
fi

declare -a CONFIG_FILES=()

add_config_file() {
	local file="$1"
	if [[ -z "$file" ]]; then
		return
	fi
	if [[ ! -f "$file" ]]; then
		echo "[WARN] Config file '$file' not found; skipping" >&2
		return
	fi
	CONFIG_FILES+=("$file")
}

# Parse explicit list (comma or newline-separated)
if [[ -n "$CONFIG_LIST_RAW" ]]; then
	while IFS= read -r entry; do
		trimmed="$(printf '%s' "$entry" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
		if [[ -n "$trimmed" ]]; then
			add_config_file "$trimmed"
		fi
	done <<< "$(printf '%s' "$CONFIG_LIST_RAW" | tr ',' '\n')"
fi

# Fallback to historical single-config behavior when no directory and no explicit list
if [[ -z "$CONFIG_DIR" && ${#CONFIG_FILES[@]} -eq 0 ]]; then
	add_config_file "$DEFAULT_CONFIG"
fi

if [[ -z "$CONFIG_DIR" && ${#CONFIG_FILES[@]} -eq 0 ]]; then
	echo "[ERROR] No backup configs discovered. Set APP_BACKUP_CONFIG_DIR or APP_BACKUP_CONFIGS." >&2
	exit 1
fi

mkdir -p "$LOGDIR"

EXTRA_FLAGS=()
if [[ -n "${APP_BACKUP_EXTRA_FLAGS:-}" ]]; then
	# shellcheck disable=SC2206 # allow intentional splitting for user-supplied flags
	EXTRA_FLAGS=( ${APP_BACKUP_EXTRA_FLAGS} )
fi

LOWER_STOP=$(echo "$STOP_ON_ERROR" | tr '[:upper:]' '[:lower:]')

run_backups() {
	printf '[%s] Starting custom app backups on %s' "$(date -Is)" "$HOSTNAME"
	if [[ -n "$CONFIG_DIR" ]]; then
		printf ' (directory: %s' "$CONFIG_DIR"
		if [[ ${#CONFIG_FILES[@]} -gt 0 ]]; then
			printf ', plus %d standalone files' "${#CONFIG_FILES[@]}"
		fi
		printf ')\n'
	else
		printf ' (%d config file(s))\n' "${#CONFIG_FILES[@]}"
	fi

	base_cmd=(
		ciwg-cli backup create "$HOSTNAME"
		--local
		--prune --remainder "$REMAINDER"
		--respect-capacity-limit --capacity-threshold "$CAPACITY_THRESHOLD"
		"${EXTRA_FLAGS[@]}"
	)

	run_cmd() {
		desc="$1"
		shift
		printf '[%s] → %s\n' "$(date -Is)" "$desc"
		if ! "$@"; then
			status=$?
			printf '[%s] !! Backup failed for %s (exit %d)\n' "$(date -Is)" "$desc" "$status"
			if [[ "$LOWER_STOP" == "true" || "$LOWER_STOP" == "1" ]]; then
				return "$status"
			fi
		else
			printf '[%s] ✓ Completed %s\n' "$(date -Is)" "$desc"
		fi
		printf '\n'
		return 0
	}

	if [[ -n "$CONFIG_DIR" ]]; then
		cmd=("${base_cmd[@]}" --config-dir "$CONFIG_DIR")
		run_cmd "config dir: $CONFIG_DIR" "${cmd[@]}" || return $?
	fi

	for config in "${CONFIG_FILES[@]}"; do
		cmd=("${base_cmd[@]}" --config-file "$config")
		run_cmd "$config" "${cmd[@]}" || return $?
		# Small pause to avoid hammering Docker metadata on large batches
		sleep 2
	end

	printf '[%s] Finished custom app backups\n' "$(date -Is)"
}

run_backups >> "$LOGFILE" 2>&1 &

exit 0