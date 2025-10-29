#!/bin/bash
# example-export-usage.sh - Demonstrates compose export feature

set -e

echo "=== Docker Compose Export Examples ==="
echo ""

# Example 1: Basic export to stdout
echo "1. Export image and restart policy to stdout:"
echo "   ciwg-cli compose export hostname --container wp_mysite --service web --keys 'image,restart'"
echo ""

# Example 2: Export to file
echo "2. Export configuration to local file:"
echo "   ciwg-cli compose export hostname \\"
echo "     --container wp_mysite \\"
echo "     --service web \\"
echo "     --keys 'image,ports,environment' \\"
echo "     --out-file web-config.env"
echo ""

# Example 3: Append placeholders to remote compose file
echo "3. Append placeholders to remote docker-compose.yml:"
echo "   ciwg-cli compose export hostname \\"
echo "     --container wp_mysite \\"
echo "     --service app \\"
echo "     --keys 'image,restart' \\"
echo "     --remote-append"
echo ""
echo "   This appends:"
echo "   # %%PLACEHOLDERS%% - exported placeholders"
echo "   placeholders:"
echo "     APP_IMAGE: '%%APP_IMAGE%%'"
echo "     APP_RESTART: '%%APP_RESTART%%'"
echo ""

# Example 4: Execute callback script
echo "4. Execute callback with exported values:"
cat > /tmp/example-callback.sh << 'CALLBACK'
#!/bin/bash
echo "=== Callback Script Received ==="
echo "Image: $WEB_IMAGE"
echo "Ports: $WEB_PORTS"
echo "Environment: $WEB_ENVIRONMENT"
# Use these values for further processing
./deploy-monitoring.sh --image "$WEB_IMAGE" || true
CALLBACK
chmod +x /tmp/example-callback.sh

echo "   ciwg-cli compose export hostname \\"
echo "     --container wp_mysite \\"
echo "     --service web \\"
echo "     --keys 'image,ports,environment' \\"
echo "     --callback /tmp/example-callback.sh"
echo ""

# Example 5: Custom prefix
echo "5. Use custom placeholder prefix:"
echo "   ciwg-cli compose export hostname \\"
echo "     --container wp_mysite \\"
echo "     --service database \\"
echo "     --keys 'image,environment' \\"
echo "     --placeholder-prefix 'DB' \\"
echo "     --out-file database.env"
echo ""
echo "   Creates: DB_IMAGE and DB_ENVIRONMENT"
echo ""

# Example 6: Server range
echo "6. Export across multiple servers:"
echo "   ciwg-cli compose export \\"
echo "     --server-range='wp%d.example.com:0-5' \\"
echo "     --container wp_site \\"
echo "     --service app \\"
echo "     --keys 'image,restart' \\"
echo "     --out-file app-config.env \\"
echo "     --callback ./aggregate-configs.sh"
echo ""

# Example 7: Append to custom remote file
echo "7. Append to custom remote YAML file:"
echo "   ciwg-cli compose export hostname \\"
echo "     --container wp_mysite \\"
echo "     --service web \\"
echo "     --keys 'image,ports' \\"
echo "     --remote-append \\"
echo "     --remote-file /var/opt/site/deployment-config.yml"
echo ""

# Example 8: Complete workflow
echo "8. Complete CI/CD workflow:"
cat > /tmp/example-workflow.sh << 'WORKFLOW'
#!/bin/bash
set -e

# Export current production config
ciwg-cli compose export prod.example.com \
  --container wp_production \
  --service app \
  --keys "image,environment,volumes" \
  --out-file prod-config-$(date +%Y%m%d).env

# Validate configuration
ciwg-cli compose export prod.example.com \
  --container wp_production \
  --service app \
  --keys "image,environment" \
  --callback ./validate-config.sh

echo "✓ Configuration exported and validated"
WORKFLOW
chmod +x /tmp/example-workflow.sh

echo "   ./example-workflow.sh"
echo ""

echo "=== Real Usage Example ==="
echo ""
echo "To test with your own server:"
echo "  ciwg-cli compose export your-server.com \\"
echo "    --container wp_yoursite \\"
echo "    --service web \\"
echo "    --keys 'image,restart,ports' \\"
echo "    --out-file test-export.env"
echo ""

echo "To see all options:"
echo "  ciwg-cli compose export --help"
echo ""

# Cleanup
rm -f /tmp/example-callback.sh /tmp/example-workflow.sh

echo "✓ Examples displayed successfully"
