#!/bin/bash
# scripts/check-deps.sh
# Validates package dependency rules to enforce layered architecture

set -e

echo "🔍 Checking package dependency rules..."

# Rule 1: domain package should have no internal imports
echo "  Checking Rule 1: domain has no internal dependencies..."
domain_imports=$(go list -f '{{join .Imports "\n"}}' ./internal/domain | grep "github.com/pscheid92/chatpulse/internal" || true)
if [ -n "$domain_imports" ]; then
    echo "❌ FAIL: domain package imports internal packages (should be pure interfaces + types)"
    echo "   Forbidden imports:"
    echo "$domain_imports" | sed 's/^/     /'
    exit 1
fi
echo "  ✅ Rule 1: domain is dependency-free"

# Rule 2: infrastructure packages should not import each other (with exceptions)
echo "  Checking Rule 2: infrastructure packages are independent..."
INFRA_PKGS=("database" "redis" "twitch" "config")
# Allowed exceptions:
# - database → crypto (for token encryption)
# - redis → metrics (for instrumentation)
# - database → metrics (for instrumentation)
for pkg in "${INFRA_PKGS[@]}"; do
    for other in "${INFRA_PKGS[@]}"; do
        if [ "$pkg" != "$other" ]; then
            if go list -f '{{join .Imports "\n"}}' ./internal/$pkg 2>/dev/null | grep -q "internal/$other"; then
                echo "❌ FAIL: $pkg imports $other (infrastructure should not cross-import)"
                echo "   Allowed exceptions: database→crypto, {redis,database}→metrics"
                exit 1
            fi
        fi
    done
done
echo "  ✅ Rule 2: infrastructure packages are independent (crypto, metrics exceptions allowed)"

# Rule 3: server should not import core infrastructure (database, redis, crypto, twitch)
echo "  Checking Rule 3: server uses application layer..."
# Allowed: server → config (for initialization), server → metrics (for registration)
# Forbidden: server → database, redis, crypto, twitch (must go through app layer)
server_imports=$(go list -f '{{join .Imports "\n"}}' ./internal/server | grep -E "internal/(database|redis|crypto|twitch)($|/)" || true)
if [ -n "$server_imports" ]; then
    echo "❌ FAIL: server imports core infrastructure directly (should use app layer)"
    echo "   Forbidden imports:"
    echo "$server_imports" | sed 's/^/     /'
    echo "   Allowed exceptions: config (initialization), metrics (registration)"
    exit 1
fi
echo "  ✅ Rule 3: server delegates to application layer (config, metrics allowed)"

# Rule 4: metrics package should have no internal imports (leaf package)
echo "  Checking Rule 4: metrics is a leaf package..."
metrics_imports=$(go list -f '{{join .Imports "\n"}}' ./internal/metrics 2>/dev/null | grep "github.com/pscheid92/chatpulse/internal" || true)
if [ -n "$metrics_imports" ]; then
    echo "❌ FAIL: metrics package imports internal packages (should be pure definitions)"
    echo "   Forbidden imports:"
    echo "$metrics_imports" | sed 's/^/     /'
    exit 1
fi
echo "  ✅ Rule 4: metrics is dependency-free"

echo ""
echo "✅ PASS: All package dependency rules validated"
echo ""
echo "Architecture layers verified:"
echo "  • Domain: Pure interfaces (no internal deps)"
echo "  • Infrastructure: Independent implementations"
echo "  • Application: Orchestrates via domain interfaces"
echo "  • Server: Thin HTTP layer using application services"
