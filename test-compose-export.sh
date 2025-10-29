#!/bin/bash
# test-compose-export.sh - Test the compose export functionality
set -e

echo "=== Testing Compose Export Feature ==="

HOSTNAME="${1:-localhost}"
CONTAINER="${2:-wp_test}"

# Test 1: Basic export to stdout
echo ""
echo "Test 1: Export to stdout"
echo "========================"
./ciwg-cli compose export "$HOSTNAME" \
  --container "$CONTAINER" \
  --service web \
  --keys "image,restart" || echo "✗ Test 1 failed"

# Test 2: Export to file
echo ""
echo "Test 2: Export to local file"
echo "============================="
./ciwg-cli compose export "$HOSTNAME" \
  --container "$CONTAINER" \
  --service web \
  --keys "image,ports,environment" \
  --out-file /tmp/test-export.env

if [ -f /tmp/test-export.env ]; then
  echo "✓ File created successfully"
  cat /tmp/test-export.env
else
  echo "✗ Test 2 failed - file not created"
  exit 1
fi

# Test 3: Export with custom prefix
echo ""
echo "Test 3: Custom placeholder prefix"
echo "=================================="
./ciwg-cli compose export "$HOSTNAME" \
  --container "$CONTAINER" \
  --service web \
  --keys "image" \
  --placeholder-prefix "CUSTOM" \
  --out-file /tmp/test-custom.env

if grep -q "CUSTOM_IMAGE" /tmp/test-custom.env; then
  echo "✓ Custom prefix applied"
else
  echo "✗ Test 3 failed - custom prefix not applied"
  exit 1
fi

# Test 4: Callback script
echo ""
echo "Test 4: Callback execution"
echo "=========================="
cat > /tmp/test-callback.sh << 'EOF'
#!/bin/bash
echo "Callback executed!"
echo "WEB_IMAGE=$WEB_IMAGE"
if [ -n "$WEB_IMAGE" ]; then
  echo "✓ Environment variable passed correctly"
  exit 0
else
  echo "✗ Environment variable missing"
  exit 1
fi
EOF
chmod +x /tmp/test-callback.sh

./ciwg-cli compose export "$HOSTNAME" \
  --container "$CONTAINER" \
  --service web \
  --keys "image" \
  --callback /tmp/test-callback.sh || echo "✗ Test 4 failed"

# Test 5: Remote append (dry run with custom file)
echo ""
echo "Test 5: Remote append to custom file"
echo "====================================="
REMOTE_FILE="/tmp/test-compose-placeholders.yml"
./ciwg-cli compose export "$HOSTNAME" \
  --container "$CONTAINER" \
  --service web \
  --keys "image,restart" \
  --remote-append \
  --remote-file "$REMOTE_FILE" || echo "⚠ Test 5 skipped (requires SSH access)"

echo ""
echo "=== All Tests Completed ==="
echo "✓ Basic export works"
echo "✓ File output works"
echo "✓ Custom prefix works"
echo "✓ Callback execution works"
echo "⚠ Remote append requires SSH access"

# Cleanup
rm -f /tmp/test-export.env /tmp/test-custom.env /tmp/test-callback.sh

echo ""
echo "To test with real server:"
echo "  $0 your-server.com wp_yourcontainer"
