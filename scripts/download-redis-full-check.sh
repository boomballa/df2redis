#!/bin/bash

set -e

echo "🔧 开始下载 redis-full-check..."
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# 确定项目根目录
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
BIN_DIR="$PROJECT_ROOT/bin"

# 创建 bin 目录
mkdir -p "$BIN_DIR"

# 检测操作系统和架构
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
        echo "❌ 不支持的操作系统: $OS"
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
        echo "❌ 不支持的架构: $ARCH"
        exit 1
        ;;
esac

echo "✓ 检测到系统: $OS_NAME $ARCH_NAME"

# Redis-full-check 下载 URL（从 Release 页面）
# 注意：redis-full-check 可能没有预编译版本，需要从源码编译
echo ""
echo "方式1: 从源码编译 (推荐)"
echo "----------------------------------------"
echo ""
echo "由于 redis-full-check 没有提供预编译的 Release 版本，"
echo "需要从源码编译。请按以下步骤操作："
echo ""
echo "1. 克隆仓库:"
echo "   git clone https://github.com/alibaba/RedisFullCheck.git"
echo "   cd RedisFullCheck"
echo ""
echo "2. 编译 (需要 Go 1.16+):"
echo "   ./build.sh"
echo ""
echo "3. 复制二进制文件:"
echo "   cp bin/redis-full-check $BIN_DIR/"
echo ""
echo "或者在 Linux 服务器上执行以下一键命令:"
echo "----------------------------------------"
cat << 'LINUX_CMD'
cd /tmp && \
git clone --depth=1 https://github.com/alibaba/RedisFullCheck.git && \
cd RedisFullCheck && \
./build.sh && \
cp bin/redis-full-check /path/to/df2redis/bin/ && \
chmod +x /path/to/df2redis/bin/redis-full-check && \
cd / && rm -rf /tmp/RedisFullCheck && \
echo "✓ redis-full-check 安装成功"
LINUX_CMD

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "⚠ 提示: 请手动在 Linux 服务器上编译 redis-full-check"
echo ""
echo "完成后验证安装:"
echo "  ./bin/redis-full-check --version"
echo ""
echo "使用示例:"
echo "  ./bin/df2redis check --config config.yaml"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
