# redis-full-check 安装指南

[English Version](en/redis-full-check-setup.md) | [中文版](redis-full-check-setup.md)

## 方式1：从源码编译（推荐）

```bash
# 1. 克隆仓库
cd /tmp
git clone https://github.com/alibaba/RedisFullCheck.git
cd RedisFullCheck

# 2. 编译
make

# 3. 将编译好的二进制文件复制到 df2redis 项目
cp redis-full-check /path/to/df2redis/bin/

# 或者复制到系统 PATH
sudo cp redis-full-check /usr/local/bin/
```

## 方式2：直接下载 Release（如果有）

```bash
# 检查 Release 页面是否有预编译版本
# https://github.com/alibaba/RedisFullCheck/releases

# 下载并解压（示例）
wget https://github.com/alibaba/RedisFullCheck/releases/download/vX.X.X/redis-full-check-linux-amd64.tar.gz
tar -xzf redis-full-check-linux-amd64.tar.gz
cp redis-full-check /path/to/df2redis/bin/
```

## 方式3：使用 df2redis 提供的下载脚本

```bash
# 在 df2redis 项目根目录执行
./scripts/download-redis-full-check.sh
```

## 验证安装

```bash
# 检查版本
redis-full-check --version

# 或者使用完整路径
./bin/redis-full-check --version
```

## 常见问题

### Q: 编译失败
A: 确保已安装 Go 1.16+ 和 make 工具

### Q: 找不到 redis-full-check 命令
A: 在 df2redis 配置文件中指定完整路径：
```yaml
check:
  redis_full_check_binary: "/path/to/redis-full-check"
```
