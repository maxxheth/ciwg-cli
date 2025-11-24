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

ciwg-cli backup create "$HOSTNAME" --prune --remainder 4 --local --capacity-threshold 80 --respect-capacity-limit >> "$LOGDIR/minio-app-backup.log" 2>&1 &

exit 0