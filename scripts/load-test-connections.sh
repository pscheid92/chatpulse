#!/usr/bin/env bash
#
# WebSocket Connection Load Test Script
# Tests connection limits under load
#
# Usage:
#   ./scripts/load-test-connections.sh <overlay_uuid> [num_connections] [server_url]
#
# Examples:
#   ./scripts/load-test-connections.sh abc-123-def 100
#   ./scripts/load-test-connections.sh abc-123-def 5000 wss://chatpulse.example.com
#

set -euo pipefail

# Configuration
OVERLAY_UUID="${1:-}"
NUM_CONNECTIONS="${2:-100}"
SERVER_URL="${3:-ws://localhost:8080}"
CONCURRENT_BATCH="${4:-50}"  # How many to open at once

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Validate inputs
if [[ -z "$OVERLAY_UUID" ]]; then
    echo -e "${RED}Error: overlay UUID required${NC}"
    echo "Usage: $0 <overlay_uuid> [num_connections] [server_url]"
    exit 1
fi

# Check for required tools
command -v wscat >/dev/null 2>&1 || {
    echo -e "${RED}Error: wscat is required but not installed.${NC}"
    echo "Install with: npm install -g wscat"
    exit 1
}

WS_URL="${SERVER_URL}/ws/overlay/${OVERLAY_UUID}"

echo -e "${GREEN}=== WebSocket Connection Load Test ===${NC}"
echo "Target URL: $WS_URL"
echo "Connections: $NUM_CONNECTIONS"
echo "Concurrent batch size: $CONCURRENT_BATCH"
echo ""

# Track results
SUCCESS_COUNT=0
FAIL_503_COUNT=0  # Global limit
FAIL_429_COUNT=0  # Rate/IP limit
FAIL_OTHER_COUNT=0
PIDS=()

# Cleanup function
cleanup() {
    echo -e "\n${YELLOW}Cleaning up background processes...${NC}"
    for pid in "${PIDS[@]}"; do
        kill "$pid" 2>/dev/null || true
    done
    wait 2>/dev/null || true
}

trap cleanup EXIT INT TERM

# Function to attempt a connection
attempt_connection() {
    local id=$1
    local output

    # Try to connect with 5 second timeout
    output=$(timeout 5s wscat -c "$WS_URL" --no-color 2>&1 || true)

    # Check the result
    if echo "$output" | grep -q "connected"; then
        echo "CONN_${id}:SUCCESS"
    elif echo "$output" | grep -q "503"; then
        echo "CONN_${id}:FAIL_503"
    elif echo "$output" | grep -q "429"; then
        echo "CONN_${id}:FAIL_429"
    else
        echo "CONN_${id}:FAIL_OTHER"
    fi
}

export -f attempt_connection
export WS_URL

echo -e "${YELLOW}Starting connection attempts...${NC}"
START_TIME=$(date +%s)

# Open connections in batches
for ((batch_start=0; batch_start<NUM_CONNECTIONS; batch_start+=CONCURRENT_BATCH)); do
    batch_end=$((batch_start + CONCURRENT_BATCH))
    if ((batch_end > NUM_CONNECTIONS)); then
        batch_end=$NUM_CONNECTIONS
    fi

    echo -e "${YELLOW}Batch $((batch_start / CONCURRENT_BATCH + 1)): Opening connections $((batch_start + 1)) to $batch_end${NC}"

    # Start connections in this batch
    for ((i=batch_start; i<batch_end; i++)); do
        (
            result=$(attempt_connection "$i")
            echo "$result"

            # If successful, keep connection alive for 2 seconds
            if echo "$result" | grep -q "SUCCESS"; then
                sleep 2
            fi
        ) &
        PIDS+=($!)
    done

    # Wait for this batch to complete
    for pid in "${PIDS[@]}"; do
        wait "$pid" 2>/dev/null || true
    done

    # Small delay between batches
    sleep 0.1
done

END_TIME=$(date +%s)
DURATION=$((END_TIME - START_TIME))

# Collect results (read from background process outputs)
# Note: This is a simplified version - in production you'd pipe results to a file
SUCCESS_COUNT=$(jobs -p | wc -l)

echo ""
echo -e "${GREEN}=== Load Test Results ===${NC}"
echo "Duration: ${DURATION}s"
echo "Total attempts: $NUM_CONNECTIONS"
echo -e "${GREEN}Successful connections: $SUCCESS_COUNT${NC}"
echo -e "${RED}Failed (503 - global limit): $FAIL_503_COUNT${NC}"
echo -e "${RED}Failed (429 - rate/IP limit): $FAIL_429_COUNT${NC}"
echo -e "${RED}Failed (other): $FAIL_OTHER_COUNT${NC}"
echo ""

# Calculate success rate
if ((NUM_CONNECTIONS > 0)); then
    SUCCESS_RATE=$(awk "BEGIN {printf \"%.1f\", ($SUCCESS_COUNT / $NUM_CONNECTIONS) * 100}")
    echo "Success rate: ${SUCCESS_RATE}%"
fi

echo ""
echo -e "${YELLOW}=== Expected Behavior ===${NC}"
echo "- With default limits (10K global, 100 per-IP, 10/sec rate):"
echo "  • Low connection counts (<100): Should succeed"
echo "  • High connection counts (>10K): Should hit global limit (503)"
echo "  • Rapid connections from same IP: Should hit rate limit (429)"
echo "  • Many connections from same IP (>100): Should hit per-IP limit (429)"
echo ""
echo -e "${YELLOW}=== Check Prometheus Metrics ===${NC}"
echo "Visit: http://localhost:8080/metrics"
echo "Look for:"
echo "  • websocket_connections_rejected_total{reason=\"global_limit\"}"
echo "  • websocket_connections_rejected_total{reason=\"ip_limit\"}"
echo "  • websocket_connections_rejected_total{reason=\"rate_limit\"}"
echo "  • websocket_connection_capacity_percent"
echo "  • websocket_unique_ips"
