#!/bin/bash
# Generate all architecture diagrams in SVG and PNG formats

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="$SCRIPT_DIR/../images/architecture"

echo "🎨 Generating architecture diagrams..."
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# Create output directory if not exists
mkdir -p "$OUTPUT_DIR"

# Check if mmdc is installed
if ! command -v mmdc &> /dev/null; then
    echo "❌ Error: mermaid-cli (mmdc) not found"
    echo "   Install with: npm install -g @mermaid-js/mermaid-cli"
    exit 1
fi

echo ""
echo "📊 Generating Replication Protocol Diagrams..."
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# Replication Protocol - Chinese
echo "  • replication-protocol-zh.svg (2800x3200)"
mmdc -i "$SCRIPT_DIR/replication-protocol-zh.mmd" \
     -o "$OUTPUT_DIR/replication-protocol-zh.svg" \
     -w 2800 -H 3200 -b transparent

echo "  • replication-protocol-zh.png (2800x3200)"
mmdc -i "$SCRIPT_DIR/replication-protocol-zh.mmd" \
     -o "$OUTPUT_DIR/replication-protocol-zh.png" \
     -w 2800 -H 3200 -b transparent

# Replication Protocol - English
echo "  • replication-protocol-en.svg (2800x3200)"
mmdc -i "$SCRIPT_DIR/replication-protocol-en.mmd" \
     -o "$OUTPUT_DIR/replication-protocol-en.svg" \
     -w 2800 -H 3200 -b transparent

echo "  • replication-protocol-en.png (2800x3200)"
mmdc -i "$SCRIPT_DIR/replication-protocol-en.mmd" \
     -o "$OUTPUT_DIR/replication-protocol-en.png" \
     -w 2800 -H 3200 -b transparent

echo ""
echo "🔄 Generating State Machine Diagrams..."
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# State Machine - Chinese
echo "  • state-machine-diagram-zh.svg (2400x3600)"
mmdc -i "$SCRIPT_DIR/state-machine-diagram-zh.mmd" \
     -o "$OUTPUT_DIR/state-machine-diagram-zh.svg" \
     -w 2400 -H 3600 -b transparent

echo "  • state-machine-diagram-zh.png (2400x3600)"
mmdc -i "$SCRIPT_DIR/state-machine-diagram-zh.mmd" \
     -o "$OUTPUT_DIR/state-machine-diagram-zh.png" \
     -w 2400 -H 3600 -b transparent

# State Machine - English
echo "  • state-machine-diagram-en.svg (2400x3600)"
mmdc -i "$SCRIPT_DIR/state-machine-diagram-en.mmd" \
     -o "$OUTPUT_DIR/state-machine-diagram-en.svg" \
     -w 2400 -H 3600 -b transparent

echo "  • state-machine-diagram-en.png (2400x3600)"
mmdc -i "$SCRIPT_DIR/state-machine-diagram-en.mmd" \
     -o "$OUTPUT_DIR/state-machine-diagram-en.png" \
     -w 2400 -H 3600 -b transparent

echo ""
echo "📋 Copying default versions (Chinese)..."
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

cp "$OUTPUT_DIR/replication-protocol-zh.svg" "$OUTPUT_DIR/replication-protocol.svg"
cp "$OUTPUT_DIR/replication-protocol-zh.png" "$OUTPUT_DIR/replication-protocol.png"
cp "$OUTPUT_DIR/state-machine-diagram-zh.svg" "$OUTPUT_DIR/state-machine-diagram.svg"
cp "$OUTPUT_DIR/state-machine-diagram-zh.png" "$OUTPUT_DIR/state-machine-diagram.png"

echo "  ✓ replication-protocol.svg"
echo "  ✓ replication-protocol.png"
echo "  ✓ state-machine-diagram.svg"
echo "  ✓ state-machine-diagram.png"

echo ""
echo "✅ All diagrams generated successfully!"
echo ""
echo "📁 Output directory: $OUTPUT_DIR"
echo ""
echo "Generated files:"
ls -lh "$OUTPUT_DIR"/*.{svg,png} 2>/dev/null | awk '{printf "  %s  %s\n", $5, $9}'

echo ""
echo "🎉 Done!"
