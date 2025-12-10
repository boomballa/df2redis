#!/bin/bash

set -e

echo "🔧 Starting redis-full-check download..."
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# Determine project root
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
BIN_DIR="$PROJECT_ROOT/bin"

# Create bin directory
mkdir -p "$BIN_DIR"

# Detect operating system and architecture
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$OS" in
    linux)
        OS_NAME="linux"
        ;;
    darwin)
        OS_NAME="darwin"
        ;;
    *)
        echo "❌ Unsupported operating system: $OS"
        exit 1
        ;;
esac

case "$ARCH" in
    x86_64)
        ARCH_NAME="amd64"
        ;;
    aarch64|arm64)
        ARCH_NAME="arm64"
        ;;
    *)
        echo "❌ Unsupported architecture: $ARCH"
        exit 1
        ;;
esac

echo "✓ Detected platform: $OS_NAME $ARCH_NAME"

# Redis-full-check download instructions
# NOTE: redis-full-check might not have prebuilt binaries, build from source
echo ""
echo "Method 1: Build from source (recommended)"
echo "----------------------------------------"
echo ""
echo "redis-full-check does not publish prebuilt Release binaries,"
echo "so you must compile it from source. Steps:"
echo ""
echo "1. Clone the repository:"
echo "   git clone https://github.com/alibaba/RedisFullCheck.git"
echo "   cd RedisFullCheck"
echo ""
echo "2. Build (requires Go 1.16+):"
echo "   ./build.sh"
echo ""
echo "3. Copy the binary:"
echo "   cp bin/redis-full-check $BIN_DIR/"
echo ""
echo "Or run the following one-liner on a Linux server:"
echo "----------------------------------------"
cat << 'LINUX_CMD'
cd /tmp && \
git clone --depth=1 https://github.com/alibaba/RedisFullCheck.git && \
cd RedisFullCheck && \
./build.sh && \
cp bin/redis-full-check /path/to/df2redis/bin/ && \
chmod +x /path/to/df2redis/bin/redis-full-check && \
cd / && rm -rf /tmp/RedisFullCheck && \
echo "✓ redis-full-check installed successfully"
LINUX_CMD

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "⚠ Note: Please compile redis-full-check manually on a Linux server"
echo ""
echo "Verify installation afterwards:"
echo "  ./bin/redis-full-check --version"
echo ""
echo "Usage example:"
echo "  ./bin/df2redis check --config config.yaml"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
