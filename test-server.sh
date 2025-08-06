#!/bin/bash
# filepath: /var/www/ciwg-cli/test-server.sh

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Default values
SERVER_HOST="localhost"
SERVER_PORT="8080"
BASE_URL="http://${SERVER_HOST}:${SERVER_PORT}"
INVENTORY_FILE="inventory.json"

# Helper functions
print_test() {
    echo -e "${BLUE}[TEST]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[PASS]${NC} $1"
}

print_fail() {
    echo -e "${RED}[FAIL]${NC} $1"
}

print_info() {
    echo -e "${YELLOW}[INFO]${NC} $1"
}

# Debug function to check server connectivity
debug_server() {
    print_info "=== SERVER DEBUG INFORMATION ==="
    
    # Check if port is open
    print_info "Checking if port ${SERVER_PORT} is open..."
    if nc -z ${SERVER_HOST} ${SERVER_PORT} 2>/dev/null; then
        print_success "Port ${SERVER_PORT} is open"
    else
        print_fail "Port ${SERVER_PORT} is not open or server is not running"
        return 1
    fi
    
    # Test basic connectivity
    print_info "Testing basic connectivity..."
    if curl -s --connect-timeout 5 "${BASE_URL}" > /dev/null 2>&1; then
        print_success "Can connect to server"
    else
        print_fail "Cannot connect to server"
        return 1
    fi
    
    # Check debug routes
    print_info "Checking available routes..."
    response=$(curl -s "${BASE_URL}/debug/routes" 2>/dev/null)
    if [ $? -eq 0 ]; then
        echo "$response" | jq . 2>/dev/null || echo "$response"
    else
        print_fail "Cannot access debug routes"
    fi
    
    # Check current working directory and file existence
    print_info "Current working directory: $(pwd)"
    print_info "Looking for inventory file: ${INVENTORY_FILE}"
    if [ -f "${INVENTORY_FILE}" ]; then
        print_success "Inventory file exists"
        print_info "File size: $(stat -c%s "${INVENTORY_FILE}" 2>/dev/null || stat -f%z "${INVENTORY_FILE}" 2>/dev/null) bytes"
    else
        print_fail "Inventory file does not exist"
    fi
    
    echo
}

# Test each endpoint with detailed debugging
test_endpoint() {
    local endpoint="$1"
    local method="${2:-GET}"
    
    print_test "Testing ${method} ${endpoint}"
    
    if [ "$method" = "POST" ]; then
        response=$(curl -s -w "\nHTTP_CODE:%{http_code}\nTIME_TOTAL:%{time_total}" -X POST "${BASE_URL}${endpoint}")
    else
        response=$(curl -s -w "\nHTTP_CODE:%{http_code}\nTIME_TOTAL:%{time_total}" "${BASE_URL}${endpoint}")
    fi
    
    # Extract parts
    body=$(echo "$response" | sed '/^HTTP_CODE:/,$d')
    http_code=$(echo "$response" | grep "^HTTP_CODE:" | cut -d: -f2)
    time_total=$(echo "$response" | grep "^TIME_TOTAL:" | cut -d: -f2)
    
    print_info "HTTP Code: $http_code"
    print_info "Response Time: ${time_total}s"
    
    if [ "$http_code" = "200" ]; then
        print_success "Endpoint returned HTTP 200"
        # Try to format JSON if possible
        if echo "$body" | jq . > /dev/null 2>&1; then
            print_info "Response is valid JSON"
        else
            print_info "Response is not JSON or malformed"
        fi
    elif [ "$http_code" = "202" ]; then
        print_success "Endpoint returned HTTP 202 (Accepted)"
    elif [ "$http_code" = "404" ]; then
        print_fail "Endpoint returned HTTP 404 (Not Found)"
        print_info "Response body: $body"
    else
        print_fail "Endpoint returned HTTP $http_code"
        print_info "Response body: $body"
    fi
    
    echo
}

# Main debug and test function
main() {
    echo "==================================="
    echo "   CIWG CLI Server Debug & Test"
    echo "==================================="
    echo
    
    # Debug server first
    if ! debug_server; then
        exit 1
    fi
    
    # Test all endpoints
    test_endpoint "/health"
    test_endpoint "/debug/routes"
    test_endpoint "/api/inventory/status"
    test_endpoint "/api/inventory"
    test_endpoint "/inventory.json"
    test_endpoint "/api/inventory/refresh" "POST"
    
    # Wait a bit and test inventory again
    print_info "Waiting 5 seconds then testing inventory again..."
    sleep 5
    test_endpoint "/api/inventory"
}

# Parse command line arguments
case "$1" in
    --help|-h)
        echo "Usage: $0 [options]"
        echo "Options:"
        echo "  --port PORT    Server port (default: 8080)"
        echo "  --host HOST    Server host (default: localhost)"
        echo "  --help         Show this help message"
        exit 0
        ;;
    --port)
        SERVER_PORT="$2"
        BASE_URL="http://${SERVER_HOST}:${SERVER_PORT}"
        shift 2
        ;;
    --host)
        SERVER_HOST="$2"
        BASE_URL="http://${SERVER_HOST}:${SERVER_PORT}"
        shift 2
        ;;
esac

# Run main function
main "$@"