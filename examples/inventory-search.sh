#!/bin/bash
# Example usage of ciwg inventory search command
# This script demonstrates various inventory search patterns and use cases

CIWG="./dist/ciwg-cli"
SERVER_RANGE="wp%d.ciwgserver.com:0-41"

echo "=== CIWG Inventory Search Examples ==="
echo

# Example 1: Basic wildcard search
echo "1. Basic wildcard search for sites containing 'example':"
$CIWG inventory search "example" --server-range="$SERVER_RANGE"
echo

# Example 2: Regex search for .dev domains
echo "2. Regex search for all .dev domains:"
$CIWG inventory search ".*\.dev$" --regex --server-range="$SERVER_RANGE"
echo

# Example 3: Case-sensitive search
echo "3. Case-sensitive search:"
$CIWG inventory search "Example" --case-sensitive --server-range="$SERVER_RANGE"
echo

# Example 4: Search on limited server range
echo "4. Search on servers 0-10 only:"
$CIWG inventory search "test" --server-range="wp%d.ciwgserver.com:0-10"
echo

# Example 5: Search with exclusions
echo "5. Search across servers 0-41, excluding 10, 15-17, 22:"
$CIWG inventory search "client" --server-range="wp%d.ciwgserver.com:0-41:!10,15-17,22"
echo

# Example 6: JSON output
echo "6. Export search results to JSON:"
$CIWG inventory search "example" --server-range="wp%d.ciwgserver.com:0-5" --format=json --output=search-results.json
cat search-results.json | jq '.'
echo

# Example 7: CSV output
echo "7. Export search results to CSV:"
$CIWG inventory search "test" --server-range="wp%d.ciwgserver.com:0-5" --format=csv --output=search-results.csv
head search-results.csv
echo

# Example 8: Search with list-containers action
echo "8. Search and list Docker containers:"
$CIWG inventory search "acomfort" --server-range="$SERVER_RANGE" --action="list-containers"
echo

# Example 9: Search with show-compose action
echo "9. Search and show docker-compose.yml:"
$CIWG inventory search "example" --server-range="wp%d.ciwgserver.com:0-5" --action="show-compose"
echo

# Example 10: Custom exec command
echo "10. Search and execute custom command (du -sh):"
$CIWG inventory search "example" --server-range="wp%d.ciwgserver.com:0-5" --exec="du -sh /var/opt/sites/example*"
echo

# Example 11: Local search
echo "11. Search on local server:"
$CIWG inventory search "mysite" --local
echo

# Example 12: Complex regex pattern
echo "12. Find sites starting with 'dev-' or 'staging-':"
$CIWG inventory search "^(dev|staging)-" --regex --server-range="wp%d.ciwgserver.com:0-10"
echo

# Example 13: Search with max-depth
echo "13. Deep search (max-depth=3):"
$CIWG inventory search "test" --max-depth=3 --server-range="wp%d.ciwgserver.com:0-5"
echo

# Example 14: Pipeline with jq
echo "14. Extract just site names from JSON output:"
$CIWG inventory search "example" --server-range="wp%d.ciwgserver.com:0-5" --format=json | \
  jq -r '.[].matches[] | split("/")[-1]'
echo

# Example 15: Count sites per server
echo "15. Count matches per server:"
$CIWG inventory search "." --regex --server-range="wp%d.ciwgserver.com:0-5" --format=json | \
  jq '.[] | {server, count: (.matches | length)}'
echo

# Cleanup
rm -f search-results.json search-results.csv

echo "=== Examples Complete ==="
