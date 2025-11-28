#!/bin/bash

# Run WordPress backups with S3 as the primary hot-storage backend.
# - requires S3 credentials via environment variables (S3_ACCESS_KEY/S3_SECRET_KEY or AWS_* defaults)
# - accepts optional S3BACKUP_* overrides for hostname, bucket metadata, logging, etc.
# - mirrors run-minio-backup.sh behavior (prune + capacity guard) but emits to S3

set -euo pipefail

HOSTNAME="${S3BACKUP_HOSTNAME:-$(hostname)}"
LOGDIR="${S3BACKUP_LOG_DIR:-/root/logs}"
LOGFILE="${S3BACKUP_LOG_FILE:-$LOGDIR/s3-backup.log}"
CAPACITY_THRESHOLD="${S3BACKUP_CAPACITY_THRESHOLD:-80}"
REMAINDER="${S3BACKUP_REMAINDER:-4}"

S3_BUCKET="${S3BACKUP_BUCKET:-${S3_BUCKET:-}}"
if [[ -z "$S3_BUCKET" ]]; then
  echo "S3 bucket not configured. Set S3BACKUP_BUCKET or S3_BUCKET." >&2
  exit 1
fi

S3_REGION="${S3BACKUP_REGION:-${S3_REGION:-us-east-1}}"
S3_ENDPOINT="${S3BACKUP_ENDPOINT:-${S3_ENDPOINT:-}}"
S3_BUCKET_PATH="${S3BACKUP_BUCKET_PATH:-${S3_BUCKET_PATH:-}}"
S3_HTTP_TIMEOUT="${S3BACKUP_HTTP_TIMEOUT:-${S3_HTTP_TIMEOUT:-}}"
S3_SSL="${S3BACKUP_SSL:-${S3_SSL:-true}}"

EXTRA_FLAGS=()
if [[ -n "${S3BACKUP_EXTRA_FLAGS:-}" ]]; then
  # shellcheck disable=SC2206 # intentional splitting to allow multiple flags
  EXTRA_FLAGS=( ${S3BACKUP_EXTRA_FLAGS} )
fi

S3_FLAGS=("--s3-bucket" "$S3_BUCKET" "--s3-region" "$S3_REGION")
if [[ -n "$S3_ENDPOINT" ]]; then
  S3_FLAGS+=("--s3-endpoint" "$S3_ENDPOINT")
fi
if [[ -n "$S3_BUCKET_PATH" ]]; then
  S3_FLAGS+=("--s3-bucket-path" "$S3_BUCKET_PATH")
fi
if [[ -n "$S3_HTTP_TIMEOUT" ]]; then
  S3_FLAGS+=("--s3-http-timeout" "$S3_HTTP_TIMEOUT")
fi
case "${S3_SSL,,}" in
  false|0)
    S3_FLAGS+=("--s3-ssl=false")
    ;;
  *)
    # default true; no flag necessary
    ;;
esac

mkdir -p "$LOGDIR"

ciwg-cli backup create "$HOSTNAME" \
  --local \
  --prune \
  --remainder "$REMAINDER" \
  --respect-capacity-limit \
  --capacity-threshold "$CAPACITY_THRESHOLD" \
  "${S3_FLAGS[@]}" \
  "${EXTRA_FLAGS[@]}" >> "$LOGFILE" 2>&1 &

exit 0
