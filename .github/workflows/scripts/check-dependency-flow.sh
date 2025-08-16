#!/bin/bash
set -euo pipefail

# Check the dependency flow and suggest next steps
# Usage: ./check-dependency-flow.sh <stage> <version>

STAGE="$1"
VERSION="$2"

case "$STAGE" in
  "core")
    echo "🔧 Core v$VERSION released!"
    echo ""
    echo "📋 Dependency Flow Status:"
    echo "✅ Core: v$VERSION (just released)"
    echo "❓ Framework: Check if update needed"
    echo "❓ Plugins: Will check after framework"
    echo "❓ Bifrost HTTP: Will check after plugins"
    echo ""
    echo "🔄 Next Step: Manually trigger Framework Release if needed"
    ;;
    
  "framework")
    echo "📦 Framework v$VERSION released!"
    echo ""
    echo "📋 Dependency Flow Status:"
    echo "✅ Core: (already updated)"
    echo "✅ Framework: v$VERSION (just released)"
    echo "❓ Plugins: Check if any need updates"
    echo "❓ Bifrost HTTP: Will check after plugins"
    echo ""
    echo "🔄 Next Step: Check Plugins Release workflow"
    ;;
    
  "plugins")
    echo "🔌 Plugins released!"
    echo ""
    echo "📋 Dependency Flow Status:"
    echo "✅ Core: (already updated)"
    echo "✅ Framework: (already updated)" 
    echo "✅ Plugins: (just released)"
    echo "❓ Bifrost HTTP: Check if update needed"
    echo ""
    echo "🔄 Next Step: Manually trigger Bifrost HTTP Release if needed"
    ;;
    
  *)
    echo "❌ Unknown stage: $STAGE"
    exit 1
    ;;
esac
