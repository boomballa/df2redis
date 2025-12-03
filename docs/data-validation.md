# 数据一致性校验

df2redis 集成了 [redis-full-check](https://github.com/alibaba/RedisFullCheck) 来提供生产级的数据一致性校验功能。

## 功能特性

- ✅ **3 种校验模式**
  - **全量值对比（full）**: 完整对比所有字段和值（最严格）
  - **键轮廓对比（outline）**: 对比 key 存在性、类型、TTL、长度等元信息（推荐）
  - **值长度对比（length）**: 只对比值的长度（最快速）

- ✅ **性能控制**
  - QPS 限制：避免对生产环境造成影响
  - 并发控制：可配置并发度

- ✅ **详细报告**
  - JSON 格式的详细结果文件
  - 不一致 key 的完整列表
  - 统计信息和耗时

## 前置要求

### 安装 redis-full-check

**方法 1：直接下载二进制文件（推荐）**

在 Linux 服务器上编译 redis-full-check：

```bash
# 1. 克隆仓库
cd /tmp
git clone https://github.com/alibaba/RedisFullCheck.git
cd RedisFullCheck

# 2. 编译
./build.sh

# 3. 复制到 df2redis 项目
cp bin/redis-full-check /path/to/df2redis/bin/
chmod +x /path/to/df2redis/bin/redis-full-check

# 4. 验证安装
/path/to/df2redis/bin/redis-full-check --version
```

**方法 2：使用系统 PATH**

```bash
# 编译后安装到系统目录
sudo cp bin/redis-full-check /usr/local/bin/
sudo chmod +x /usr/local/bin/redis-full-check

# 验证
redis-full-check --version
```

更多安装方法请参考 [redis-full-check 安装指南](redis-full-check-setup.md)。

## 使用方法

### 基本用法

```bash
# 使用默认配置（键轮廓对比模式）
./bin/df2redis check --config config.yaml

# 指定校验模式
./bin/df2redis check --config config.yaml --mode full      # 全量值对比
./bin/df2redis check --config config.yaml --mode outline   # 键轮廓对比（默认）
./bin/df2redis check --config config.yaml --mode length    # 值长度对比

# 自定义性能参数
./bin/df2redis check --config config.yaml \
  --mode outline \
  --qps 1000 \
  --parallel 8
```

### 命令行参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `--config, -c` | 配置文件路径（必需） | - |
| `--mode` | 校验模式：full/outline/length | `outline` |
| `--qps` | QPS 限制（0 表示不限制） | `500` |
| `--parallel` | 并发度 | `4` |
| `--result-dir` | 结果输出目录 | `./check-results` |
| `--binary` | redis-full-check 二进制文件路径 | `redis-full-check` |

### 配置文件示例

check 命令会从配置文件中读取源端和目标端的连接信息：

```yaml
# config.yaml
source:
  addr: "10.46.128.12:7380"      # Dragonfly 地址
  password: ""                    # 可选

target:
  type: "redis-cluster"           # 或 "redis-standalone"
  seed: "10.180.50.231:6379"      # Redis 地址
  password: "your-password"       # 可选
  tls: false
```

## 校验模式对比

### 全量值对比（full）

**适用场景**：
- 严格的数据一致性要求
- 小规模数据集
- 迁移后的最终验证

**特点**：
- ✓ 最严格、最准确
- ✗ 性能开销最大
- ✗ 耗时最长

**建议**：
- 仅在小规模数据集或最终验证时使用
- 建议限制 QPS 避免影响生产

### 键轮廓对比（outline，推荐）

**适用场景**：
- 大规模数据集的日常校验
- 持续的增量同步验证
- 生产环境快速检查

**特点**：
- ✓ 性能与准确性平衡
- ✓ 可检测大部分不一致问题
- ✓ 对生产影响小

**检查内容**：
- Key 是否存在
- 数据类型是否一致
- TTL 是否匹配
- 集合/列表/哈希等的元素数量

**建议**：
- 作为默认校验模式
- 适合定期执行

### 值长度对比（length）

**适用场景**：
- 超大规模数据集的快速预检
- 性能敏感的生产环境
- 初步一致性检查

**特点**：
- ✓ 最快速
- ✓ 对生产影响最小
- ✗ 可能漏掉某些不一致

**建议**：
- 用于快速预检
- 发现问题后再用 outline 或 full 模式详细检查

## 结果解读

### 终端输出

```
🔍 开始数据一致性校验
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  • 校验模式: 键轮廓对比 (元信息对比)
  • 源端地址: 10.46.x.x:7380
  • 目标地址: 10.180.x.x:6379
  • QPS 限制: 500
  • 并发度: 4
  • 结果文件: ./check-results/check_20251204_150405.json
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  [INFO] scan...
  [INFO] compare...
  [INFO] finish...

✓ 校验完成，耗时: 45s

📊 校验结果汇总
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  • 校验耗时: 45s
  • 不一致 key 数量: 0

✓ 数据完全一致！
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

### 发现不一致时

```
📊 校验结果汇总
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  • 校验耗时: 1m 23s
  • 不一致 key 数量: 15

⚠ 发现数据不一致
  • 结果文件: ./check-results/check_20251204_150405.json

  不一致的 key 样本（前 10 个）:
    1. user:12345:profile
    2. session:abcd1234
    3. cache:product:9876
    ... 更多 key 请查看结果文件
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

### 结果文件格式

结果文件是 JSON Lines 格式，每一行是一个不一致的 key 的详细信息：

```json
{"key":"user:12345:profile","type":"inconsistent","source_type":"hash","target_type":"hash","source_len":10,"target_len":9}
{"key":"session:abcd1234","type":"missing","source_type":"string","target_type":"","source_len":128,"target_len":0}
```

字段说明：
- `key`: 不一致的 key
- `type`: 不一致类型（inconsistent/missing/extra）
- `source_type`: 源端数据类型
- `target_type`: 目标端数据类型
- `source_len`: 源端值长度
- `target_len`: 目标端值长度

## 最佳实践

### 1. 迁移后的完整验证

```bash
# 步骤 1: 使用 outline 模式快速检查
./bin/df2redis check --config config.yaml --mode outline

# 步骤 2: 如果发现问题，使用 full 模式详细检查
./bin/df2redis check --config config.yaml --mode full --qps 200

# 步骤 3: 分析结果文件
cat ./check-results/check_*.json | jq '.'
```

### 2. 增量同步期间的定期检查

```bash
# 使用 outline 模式，限制 QPS 避免影响生产
./bin/df2redis check --config config.yaml \
  --mode outline \
  --qps 500 \
  --parallel 4
```

### 3. 大规模数据集的分阶段验证

```bash
# 阶段 1: length 模式快速预检（10 分钟）
./bin/df2redis check --config config.yaml --mode length --qps 2000

# 阶段 2: outline 模式常规检查（1 小时）
./bin/df2redis check --config config.yaml --mode outline --qps 1000

# 阶段 3: full 模式抽样检查（仅检查重要数据）
# 需要修改源端配置，只连接包含重要数据的分片
./bin/df2redis check --config config-critical.yaml --mode full --qps 100
```

### 4. 自动化脚本

```bash
#!/bin/bash
# check-and-alert.sh

CONFIG="config.yaml"
MODE="outline"
ALERT_EMAIL="ops@example.com"

# 执行校验
if ./bin/df2redis check --config "$CONFIG" --mode "$MODE"; then
    echo "✓ 数据一致性校验通过"
else
    echo "✗ 发现数据不一致" | mail -s "df2redis 数据校验告警" "$ALERT_EMAIL"

    # 上传结果文件到监控系统
    latest_result=$(ls -t ./check-results/check_*.json | head -1)
    curl -X POST https://monitoring.example.com/api/upload \
      -F "file=@$latest_result"
fi
```

## 性能调优

### QPS 限制建议

| 环境 | 数据规模 | 建议 QPS |
|------|----------|----------|
| 开发/测试 | 任意 | 不限制（0） |
| 生产环境 | < 1GB | 1000-2000 |
| 生产环境 | 1-10GB | 500-1000 |
| 生产环境 | > 10GB | 100-500 |
| 高峰时段 | 任意 | 100-200 |

### 并发度建议

- **CPU 密集型**：并发度 = CPU 核心数
- **网络密集型**：并发度 = CPU 核心数 × 2
- **混合负载**：并发度 = 4-8（默认）

### 资源占用

| 校验模式 | 内存占用 | CPU 占用 | 网络带宽 |
|----------|----------|----------|----------|
| length | 低 | 低 | 低 |
| outline | 中 | 中 | 中 |
| full | 高 | 高 | 高 |

## 故障排查

### 问题 1: redis-full-check: command not found

**原因**：未安装 redis-full-check 或未在 PATH 中

**解决**：
```bash
# 方法 1: 指定完整路径
./bin/df2redis check --config config.yaml --binary ./bin/redis-full-check

# 方法 2: 安装到系统 PATH
sudo cp bin/redis-full-check /usr/local/bin/
```

### 问题 2: 校验速度很慢

**原因**：QPS 限制太低或并发度不足

**解决**：
```bash
# 提高 QPS 和并发度（注意监控对生产的影响）
./bin/df2redis check --config config.yaml --qps 2000 --parallel 8
```

### 问题 3: 内存占用过高

**原因**：使用 full 模式或数据集过大

**解决**：
```bash
# 降级到 outline 或 length 模式
./bin/df2redis check --config config.yaml --mode outline
```

### 问题 4: 连接超时

**原因**：网络问题或 Redis 负载过高

**解决**：
```bash
# 降低 QPS 和并发度
./bin/df2redis check --config config.yaml --qps 100 --parallel 2
```

## 与其他工具的集成

### 与 CI/CD 集成

```yaml
# .gitlab-ci.yml
validate:
  stage: test
  script:
    - ./bin/df2redis check --config config.yaml --mode outline
  only:
    - main
```

### 与监控系统集成

```bash
# Prometheus metrics 导出示例
cat ./check-results/check_latest.json | jq '{
  inconsistent_keys: (.inconsistent_keys // 0),
  duration_seconds: (.duration_seconds // 0)
}' | curl -X POST http://pushgateway:9091/metrics/job/df2redis_check
```

## 参考资源

- [redis-full-check GitHub](https://github.com/alibaba/RedisFullCheck)
- [redis-full-check 安装指南](redis-full-check-setup.md)
- [df2redis 架构文档](architecture.md)
