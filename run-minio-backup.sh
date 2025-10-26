#!/bin/bash

HOSTNAME="$(hostname)"
ciwg-cli backup create "$HOSTNAME" --local > /root/logs/minio-backup.log 2>&1