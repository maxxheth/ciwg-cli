#!/bin/bash

# Run the backup in the background reliably:
# - ensure the log directory exists
# - log output to a file
# - relegate to background
# - use strict error handling

set -euo pipefail

HOSTNAME="$(hostname)"
LOGDIR="/root/logs"
mkdir -p "$LOGDIR"

ciwg-cli backup create "$HOSTNAME" --dry-run --overwrite --remainder 2 --bucket-path backups/dev.ciwebgroup.com --config-file ./dev.ciwebgroup.com.yml --local >> "$LOGDIR/minio-backup.log" 2>&1 &

exit 0