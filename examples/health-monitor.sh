#!/bin/bash
#
# health-monitor.sh - WordPress Health Monitoring Examples
#
# This script demonstrates practical usage of the CIWG CLI health monitoring system
#

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "=== CIWG CLI Health Monitoring Examples ==="
echo

# Example 1: Quick health check of a single site
echo "Example 1: Single Site Health Check"
echo "Command: ciwg health check wp0.example.com --container wp_site"
echo
# ciwg health check wp0.example.com --container wp_site
echo

# Example 2: Check all containers on a server
echo "Example 2: Check All Containers on Server"
echo "Command: ciwg health check wp0.example.com --all-containers"
echo
# ciwg health check wp0.example.com --all-containers
echo

# Example 3: Quick probe with timing details
echo "Example 3: Quick Probe with Timing"
echo "Command: ciwg health probe wp0.example.com"
echo
# ciwg health probe wp0.example.com
echo

# Example 4: Fetch PromPress metrics
echo "Example 4: Fetch PromPress Metrics"
echo "Command: ciwg health metrics wp0.example.com --container wp_site"
echo
# ciwg health metrics wp0.example.com --container wp_site
echo

# Example 5: Check server range with JSON output
echo "Example 5: Server Range Check (JSON)"
echo "Command: ciwg health check --server-range='wp%d.example.com:0-5' -o json"
echo
# ciwg health check --server-range="wp%d.example.com:0-5" -o json
echo

# Example 6: Export health metrics for Prometheus
echo "Example 6: Export to Prometheus Format"
echo "Command: ciwg health check --server-range='wp%d.example.com:0-41' -o prometheus"
echo
# ciwg health check --server-range="wp%d.example.com:0-41" -o prometheus > /tmp/health-metrics.prom
echo

# Example 7: Find unhealthy sites
echo "Example 7: Find Unhealthy Sites"
cat << 'EOF'
Command: 
ciwg health check --server-range="wp%d.example.com:0-41" -o json | \
  jq -r '.[] | select(.status != "healthy") | "\(.hostname) [\(.container)]: \(.status)"'
EOF
echo

# Example 8: SSL certificate expiration check
echo "Example 8: SSL Certificate Expiration Check"
cat << 'EOF'
Command:
ciwg health check --server-range="wp%d.example.com:0-41" -o json | \
  jq -r '.[] | select(.ssl.days_until_expiry < 30) | 
    "\(.hostname): SSL expires in \(.ssl.days_until_expiry) days"'
EOF
echo

# Example 9: High resource usage report
echo "Example 9: High Resource Usage Report"
cat << 'EOF'
Command:
ciwg health check --server-range="wp%d.example.com:0-41" -o json | \
  jq -r '.[] | select(.docker.memory_percent > 80 or .docker.cpu_percent > 80) | 
    "\(.hostname) [\(.container)]: CPU \(.docker.cpu_percent)%, MEM \(.docker.memory_percent)%"'
EOF
echo

# Example 10: Monitoring dashboard
echo "Example 10: Live Monitoring Dashboard"
echo "Command: ciwg health dashboard wp0.example.com --container wp_site --interval 5s"
echo "(Press Ctrl+C to exit)"
echo
# ciwg health dashboard wp0.example.com --container wp_site --interval 5s
echo

# Example 11: Custom health check with specific flags
echo "Example 11: Custom Health Check"
cat << 'EOF'
Command:
ciwg health check wp0.example.com \
  --container wp_site \
  --check-timeout 60s \
  --ssl-check=true \
  --prompress=true \
  --docker-stats=true \
  --verbose \
  -o json
EOF
echo

# Example 12: Probe with custom headers
echo "Example 12: Probe with Custom Headers"
cat << 'EOF'
Command:
ciwg health probe wp0.example.com \
  --header "User-Agent: CIWG-Health-Monitor/1.0" \
  --header "Accept: application/json" \
  --follow-redirects=true \
  --verify-ssl=true
EOF
echo

# Example 13: Automated monitoring script
echo "Example 13: Automated Monitoring Script"
cat << 'EOF'
#!/bin/bash
# monitor-and-alert.sh

while true; do
  echo "$(date): Checking WordPress sites..."
  
  # Check all sites and find unhealthy ones
  unhealthy=$(ciwg health check --server-range="wp%d.example.com:0-41" -o json | \
    jq -r '.[] | select(.status != "healthy") | "\(.hostname) [\(.container)]: \(.status)"')
  
  if [ -n "$unhealthy" ]; then
    echo "ALERT: Unhealthy sites detected:"
    echo "$unhealthy"
    
    # Send alert (email, Slack, etc.)
    echo "$unhealthy" | mail -s "WordPress Health Alert" admin@example.com
  else
    echo "All sites healthy"
  fi
  
  # Wait 5 minutes before next check
  sleep 300
done
EOF
echo

# Example 14: Collect performance baselines
echo "Example 14: Collect Performance Baselines"
cat << 'EOF'
#!/bin/bash
# collect-baselines.sh

output_file="baseline-$(date +%Y%m%d-%H%M%S).jsonl"

for i in {0..41}; do
  echo "Probing wp$i.example.com..."
  ciwg health probe wp$i.example.com -o json >> "$output_file"
done

echo "Baselines saved to $output_file"

# Calculate statistics
echo
echo "Performance Statistics:"
echo "Average response time: $(cat "$output_file" | jq -r '.response_time' | awk '{sum+=$1} END {print sum/NR}')s"
echo "Max response time: $(cat "$output_file" | jq -r '.response_time' | sort -n | tail -1)s"
echo "Min response time: $(cat "$output_file" | jq -r '.response_time' | sort -n | head -1)s"
EOF
echo

# Example 15: Push metrics to Prometheus Pushgateway
echo "Example 15: Push Metrics to Prometheus Pushgateway"
cat << 'EOF'
#!/bin/bash
# push-to-prometheus.sh

PUSHGATEWAY_URL="http://pushgateway.example.com:9091"
JOB_NAME="wordpress_health"

echo "Collecting health metrics..."
ciwg health check --server-range="wp%d.example.com:0-41" -o prometheus | \
  curl --data-binary @- "$PUSHGATEWAY_URL/metrics/job/$JOB_NAME"

echo "Metrics pushed to Pushgateway"
EOF
echo

echo "=== Environment Variables ==="
echo
cat << 'EOF'
# Set these in your environment or .env file:

export SSH_USER="root"
export SSH_PORT="22"
export SSH_KEY="~/.ssh/id_rsa"
export SSH_AGENT="true"
export SSH_TIMEOUT="30s"

export SERVER_RANGE="wp%d.example.com:0-41:!10,15-17,22"

export PROMPRESS_METRICS_PATH="metrics"
export PROMPRESS_TOKEN="your-secret-token"
EOF
echo

echo "=== Quick Reference ==="
echo
echo "Health Check:  ciwg health check HOSTNAME --container CONTAINER"
echo "Probe:         ciwg health probe HOSTNAME"
echo "Metrics:       ciwg health metrics HOSTNAME --container CONTAINER"
echo "Dashboard:     ciwg health dashboard HOSTNAME --container CONTAINER"
echo
echo "Server Range:  --server-range='wp%d.example.com:0-41:!10,15'"
echo "Output:        -o text|json|prometheus"
echo
echo "See docs/HEALTH_MONITORING.md for full documentation"
