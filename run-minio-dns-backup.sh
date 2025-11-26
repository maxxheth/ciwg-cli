#!/bin/bash

# Trigger a DNS backup run in the background, mirroring run-minio-backup.sh behavior.
# - ensures logs directory exists
# - reuses env overrides for lookup source and thresholds
# - appends output to a log file while disassociating from terminal

set -euo pipefail

HOSTNAME="${DNSBACKUP_LOOKUP_HOST:-$(hostname)}"
SERVER_RANGE="${DNSBACKUP_SERVER_RANGE:-}"
CAPACITY_THRESHOLD="${DNSBACKUP_CAPACITY_THRESHOLD:-80}"

EXTRA_FLAGS=()
if [[ -n "${DNSBACKUP_EXTRA_FLAGS:-}" ]]; then
  # shellcheck disable=SC2206 # intentional splitting on whitespace for flag list
  EXTRA_FLAGS=( ${DNSBACKUP_EXTRA_FLAGS} )
fi

LOGDIR="/root/logs"
mkdir -p "$LOGDIR"
LOGFILE="$LOGDIR/minio-dns-backup.log"

ZONE_LOOKUP_FLAGS=("--zone-lookup")
TARGET_ARGS=()
if [[ -n "$SERVER_RANGE" ]]; then
  ZONE_LOOKUP_FLAGS+=("--server-range" "$SERVER_RANGE")
else
  TARGET_ARGS+=("$HOSTNAME")
fi

ciwg-cli dns-backup create "${TARGET_ARGS[@]}" "${ZONE_LOOKUP_FLAGS[@]}" \
  --respect-capacity-limit \
  --capacity-threshold "$CAPACITY_THRESHOLD" \
  "${EXTRA_FLAGS[@]}" >> "$LOGFILE" 2>&1 &

exit 0
