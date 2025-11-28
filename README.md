# Project ciwg-cli

A comprehensive command-line tool for managing WordPress infrastructure across multiple servers. Provides functionality for SSH authentication, cron job management, health monitoring, backup operations, and remote script execution.

## Features

- **Health Monitoring**: Comprehensive WordPress site health checks with PromPress metrics integration
- **Inventory Management**: Fast, parallel search for WordPress sites across servers with wildcard and regex support
- **SSH Management**: Centralized SSH authentication with agent support and keep-alive
- **Cron Management**: View and manage cron jobs across server ranges
- **Backup Operations**: Automated backup with Minio integration
- **PromPress Integration**: WordPress metrics collection and Prometheus integration
- **Compose Management**: Docker Compose configuration management
- **Server Range Support**: Execute commands across multiple servers with exclusion patterns

## Quick Start

### Installation

```bash
# Build the project
make build

# The binary will be in ./dist/ciwg-cli-utils.tgz
# Extract and use
tar -xzf dist/ciwg-cli-utils.tgz
./ciwg --help
```

### Basic Usage

```bash
# Health check on a WordPress site
ciwg health check wp0.example.com --container wp_site

# Quick probe with timing details
ciwg health probe wp0.example.com

# Fetch PromPress metrics
ciwg health metrics wp0.example.com --container wp_site

# Check across server range
ciwg health check --server-range="wp%d.example.com:0-41:!10,15"

# Search for WordPress sites
ciwg inventory search "example" --server-range="wp%d.example.com:0-41"

# Generate complete inventory
ciwg inventory generate --server-range="wp%d.example.com:0-41" --format=json

# List cron jobs
ciwg cron list hostname

# SSH connection test
ciwg ssh test hostname
```

## Commands

### Health Monitoring

Comprehensive health monitoring with PromPress integration and curl-like probing.

```bash
# Complete health check
ciwg health check [hostname] [flags]

# Fetch PromPress metrics
ciwg health metrics [hostname] [flags]

# Quick HTTP probe with timing
ciwg health probe [hostname] [flags]

# Live health dashboard
ciwg health dashboard [hostname] [flags]
```

See [Health Monitoring Guide](docs/HEALTH_MONITORING.md) for detailed documentation.

### PromPress Integration

Install and configure PromPress WordPress metrics plugin.

```bash
# Install PromPress with Prometheus integration
ciwg prompress install hostname --container wp_site

# Test metrics endpoint
ciwg prompress test-metrics hostname --container wp_site
```

See [PromPress Guide](docs/PROMPRESS_GUIDE.md) for detailed documentation.

### Inventory Management

Discover and search for WordPress sites across your infrastructure.

```bash
# Generate site inventory across server range
ciwg inventory generate --server-range="wp%d.ciwgserver.com:0-41"

# Search for sites matching a pattern
ciwg inventory search "example" --server-range="wp%d.ciwgserver.com:0-41"

# Search with regex
ciwg inventory search ".*\.dev$" --regex --server-range="wp%d.ciwgserver.com:0-10"

# Search locally
ciwg inventory search "mysite" --local

# Search and list containers
ciwg inventory search "acomfort" --server-range="wp%d.ciwgserver.com:0-41" --action="list-containers"

# Export results in different formats
ciwg inventory search "client" --server-range="wp%d.ciwgserver.com:0-41" --format=json --output=results.json
```

See [Inventory Search Guide](docs/INVENTORY_SEARCH.md) for detailed documentation.

### SSH Management

```bash
# Test SSH connection
ciwg ssh test hostname

# Connect to server
ciwg ssh connect hostname
```

### Cron Management

```bash
# List cron jobs
ciwg cron list hostname

# Edit cron jobs
ciwg cron edit hostname

# Show cron configuration
ciwg cron show hostname
```

### Backup Operations

Create, manage, and maintain backups of WordPress containers with support for MinIO and AWS S3 storage backends, intelligent retention policies, and automatic migration to AWS Glacier.

#### Storage Backends

The CLI supports two object storage backends:
- **MinIO**: Self-hosted S3-compatible storage (default)
- **AWS S3**: Amazon S3 cloud storage (alternative)

Both backends support the same feature set and can be configured via flags or environment variables.

```bash
# Backup to MinIO (traditional)
ciwg backup create hostname \
  --minio-endpoint minio.example.com:9000 \
  --minio-bucket backups

# Backup to AWS S3 (new!)
ciwg backup create hostname \
  --s3-bucket my-backups \
  --s3-region us-west-2 \
  --s3-access-key $AWS_ACCESS_KEY_ID \
  --s3-secret-key $AWS_SECRET_ACCESS_KEY

# Backup with custom config
ciwg backup create hostname --config-file backup-config.yml

# Backup server range
ciwg backup create --server-range="wp%d.example.com:0-41"
```

#### Smart Retention Policies

Configure intelligent retention policies using a YAML file to automatically:
- Keep daily backups for a specified period
- Preserve weekly backups (e.g., every Sunday)
- Preserve monthly backups (e.g., last day of each month)
- Automatically migrate old backups to AWS Glacier for long-term cold storage
- Delete backups that don't meet any retention criteria

Example retention policy (`retention-policy.yml`):
```yaml
rulesets:
  # Keep weekly backups (Sundays) and migrate to Glacier after 7 days
  weekly_to_glacier:
    older_than: "7 days"
    exclude: "Sunday"
    action: "migrate_to_glacier"
    target_storage: "both"
  
  # Delete daily backups after 7 days
  daily_cleanup:
    older_than: "7 days"
    exclude: ""
    action: "delete"
    target_storage: "both"
  
  # Keep monthly backups (last day of month) and migrate to Glacier after 6 months
  monthly_to_glacier:
    older_than: "6 months"
    exclude: "last day of month"
    action: "migrate_to_glacier"
    target_storage: "both"
  
  # Delete non-monthly backups after 6 months
  old_cleanup:
    older_than: "6 months"
    exclude: ""
    action: "delete"
    target_storage: "both"
```

Usage:
```bash
# Create backup with smart retention policy
ciwg backup create hostname \
  --prune \
  --retention-policy retention-policy.yml

# Works with server ranges
ciwg backup create --server-range="wp%d.example.com:0-41" \
  --prune \
  --retention-policy retention-policy.yml

# Works with S3
ciwg backup create hostname \
  --s3-bucket my-backups \
  --prune \
  --retention-policy retention-policy.yml
```

See `examples/retention-policy.yml` for a complete example with detailed comments.

#### Basic Backup Commands

```bash
# Create backup
ciwg backup create hostname

# List backups (MinIO)
ciwg backup list --minio-endpoint minio.example.com:9000

# List backups (S3)
ciwg backup list --s3-bucket my-backups --s3-region us-west-2

# Read/download a backup
ciwg backup read backups/site-20240101-120000.tgz --output backup.tgz

# Delete backups
ciwg backup delete backups/site-20240101-120000.tgz

# Test connections
ciwg backup conn
```

### DNS Backup

Strictly manage Cloudflare DNS zone backups with dry-run friendly workflows.

```bash
# Export DNS records to a JSON snapshot
ciwg dns-backup export example.com --output=snapshots/example.com.json

# Preview how a snapshot would change the zone (no writes)
ciwg dns-backup plan example.com \
	--snapshot snapshots/example.com.json --delete-missing --print-plan

# Apply the snapshot with deletions after reviewing the plan
ciwg dns-backup apply example.com \
	--snapshot snapshots/example.com.json --delete-missing --yes

# Validate API token permissions
ciwg dns-backup test
```

### Compose Management

```bash
# Export compose configuration
ciwg compose export hostname --container wp_site

# Manage compose files
ciwg compose start hostname --container wp_site
```

## Server Range Pattern

Many commands support server range patterns for batch operations:

```bash
# Pattern format: "hostname_pattern:start-end:!exclusions"

# Check servers 0-41, excluding 10, 15-17, and 22
--server-range="wp%d.example.com:0-41:!10,15-17,22"

# Check servers 0-10
--server-range="wp%d.example.com:0-10"
```

## Environment Variables

Configure defaults via environment variables:

```bash
# SSH Configuration
export SSH_USER="root"
export SSH_PORT="22"
export SSH_KEY="~/.ssh/id_rsa"
export SSH_AGENT="true"
export SSH_TIMEOUT="30s"

# Server Range
export SERVER_RANGE="wp%d.example.com:0-41:!10,15-17,22"

# PromPress Configuration
export PROMPRESS_METRICS_PATH="metrics"
export PROMPRESS_TOKEN="your-secret-token"

# Minio Configuration
export MINIO_ENDPOINT="minio.example.com:9000"

# Cloudflare DNS Backup
export CLOUDFLARE_DNS_BACKUP_TOKEN="cf_example_token"
export MINIO_ACCESS_KEY="access-key"
export MINIO_SECRET_KEY="secret-key"
export MINIO_BUCKET="backups"
```

## Documentation

- [Health Monitoring Guide](docs/HEALTH_MONITORING.md) - Comprehensive health monitoring
- [Health Quick Reference](docs/HEALTH_QUICKREF.md) - Quick command reference
- [PromPress Guide](docs/PROMPRESS_GUIDE.md) - PromPress installation and configuration
- [PromPress Prometheus Config](docs/PROMPRESS_PROMETHEUS_CONFIG.md) - Prometheus integration
- [Backup System](docs/CUSTOM_BACKUP_CONFIG.md) - Backup configuration
- [Compose Management](docs/COMPOSE_MANAGEMENT.md) - Docker Compose operations

## Getting Started

These instructions will get you a copy of the project up and running on your local machine for development and testing purposes.

## MakeFile

Run build make command with tests
```bash
make all
```

Build the application
```bash
make build
```

Run the application
```bash
make run
```
Create DB container
```bash
make docker-run
```

Shutdown DB Container
```bash
make docker-down
```

DB Integrations Test:
```bash
make itest
```

Live reload the application:
```bash
make watch
```

Run the test suite:
```bash
make test
```

Clean up binary from the last build:
```bash
make clean
```
