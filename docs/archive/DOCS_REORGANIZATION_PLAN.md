# 文档重组方案 (Documentation Reorganization Plan)

## 📊 当前问题分析

### 1. **Phase 文档问题**

#### 🔴 严重问题
- **英文 Phase 文档极度不完整**：只有 19-21 行占位符内容
  - `docs/en/Phase-1.md` to `Phase-7.md` 仅包含简短摘要
  - 中文版本详细（340-994 行），但英文版本未翻译

#### 🟡 内容过时
- **Phase 1-7 是开发日志**，记录了逐步实现过程
- 很多内容已经被后续优化替代
- 混杂了临时方案、调试信息、Git 提交示例

#### 📝 建议
- **Phase 文档应该归档或删除**，不应作为主要文档
- 用户不需要了解开发过程，只需要了解最终架构和使用方法

---

### 2. **重复内容问题**

#### 🔄 重复的文档

| 文档 A | 文档 B | 重复程度 | 建议 |
|--------|--------|---------|------|
| `docs/zh/architecture.md` | `docs/zh/architecture/overview.md` | 高度相似 | 删除 `architecture.md`，保留 `architecture/overview.md` |
| `docs/zh/research/dragonfly-replica-protocol.md` | `docs/principle_and_implementation/Dragonfly Replica 实现详解.md` | 90% 相似 | 合并到 `research/` 目录 |
| Phase 文档中的架构描述 | `docs/zh/architecture/*` | 部分重复 | 删除 Phase 文档 |

---

### 3. **SVG 文件冗余**

#### 📊 当前 SVG 文件 (10 个)

```
replication-protocol.svg         ← 默认（中文）
replication-protocol-zh.svg      ← 中文
replication-protocol-en.svg      ← 英文

state-machine-diagram.svg        ← 默认（中文）
state-machine-diagram-zh.svg     ← 中文
state-machine-diagram-en.svg     ← 英文

cluster-routing.svg
data-pipeline.svg
multi-flow.svg
RDB+Journal-data-flow.svg
```

#### ❌ 问题
- **冗余**：默认版本 (`.svg`) 和 `-zh.svg` 完全相同
- **不一致**：有些图有中英文版本，有些没有

#### ✅ 建议
- **删除默认版本** (`replication-protocol.svg`, `state-machine-diagram.svg`)
- **统一命名**：所有图都应该有 `-zh` 和 `-en` 后缀
- **缺失图表**：为 `cluster-routing`, `data-pipeline`, `multi-flow`, `RDB+Journal-data-flow` 创建英文版本

---

### 4. **文档分类混乱**

#### 当前结构
```
docs/
├── zh/
│   ├── Phase-1.md ... Phase-7.md        ← 开发日志（过时）
│   ├── architecture.md                  ← 与 architecture/overview.md 重复
│   ├── architecture/                    ← 架构文档（主要）
│   ├── research/                        ← 研究笔记
│   ├── dashboard.md                     ← 零散文档
│   ├── data-validation.md
│   ├── Dragonfly-v1.36.0-Bug-Workaround.md  ← 已过时（官方修复）
│   ├── fix-rdb-completion-race-condition.md
│   ├── issue-type-assertion-performance.md
│   └── redis-full-check-setup.md
├── en/
│   ├── Phase-1.md ... Phase-7.md        ← 占位符（未完成）
│   ├── architecture.md
│   ├── architecture/                    ← 架构文档（主要）
│   └── ... (同 zh/)
└── principle_and_implementation/        ← 与 research/ 重复
```

#### ❌ 问题
1. **根目录文档零散**：`dashboard.md`, `data-validation.md` 等应该归类
2. **principle_and_implementation** 目录与 `research/` 功能重复
3. **过时文档未清理**：`Dragonfly-v1.36.0-Bug-Workaround.md`（官方已修复）

---

## 🎯 重组方案

### 目标结构

```
docs/
├── README.md                          ← 文档索引（新建）
├── diagrams/                          ← Mermaid 源文件
│   ├── README.md
│   ├── generate.sh
│   ├── replication-protocol-zh.mmd
│   ├── replication-protocol-en.mmd
│   ├── state-machine-diagram-zh.mmd
│   └── state-machine-diagram-en.mmd
├── images/
│   └── architecture/
│       ├── replication-protocol-zh.svg
│       ├── replication-protocol-en.svg
│       ├── state-machine-diagram-zh.svg
│       ├── state-machine-diagram-en.svg
│       ├── cluster-routing-zh.svg
│       ├── cluster-routing-en.svg
│       ├── data-pipeline-zh.svg
│       ├── data-pipeline-en.svg
│       ├── multi-flow-zh.svg
│       ├── multi-flow-en.svg
│       ├── overview-zh.svg
│       └── overview-en.svg
├── zh/
│   ├── README.md                      ← 中文文档索引
│   ├── architecture/                  ← 架构设计（主要文档）
│   │   ├── overview.md
│   │   ├── replication-protocol.md
│   │   ├── multi-flow.md
│   │   ├── data-pipeline.md
│   │   └── cluster-routing.md
│   ├── guides/                        ← 使用指南（新建）
│   │   ├── installation.md
│   │   ├── configuration.md
│   │   ├── dashboard.md
│   │   └── data-validation.md
│   ├── troubleshooting/               ← 故障排查（新建）
│   │   ├── redis-full-check-setup.md
│   │   └── common-issues.md
│   └── research/                      ← 深入研究（高级用户）
│       ├── dragonfly-replica-protocol.md
│       ├── dragonfly-rdb-format.md
│       ├── dragonfly-stream-sync.md
│       └── fullsync-performance.md
├── en/
│   ├── README.md                      ← English docs index
│   ├── architecture/                  ← Architecture design
│   │   ├── overview.md
│   │   ├── replication-protocol.md
│   │   ├── multi-flow.md
│   │   ├── data-pipeline.md
│   │   └── cluster-routing.md
│   ├── guides/                        ← User guides
│   │   ├── installation.md
│   │   ├── configuration.md
│   │   ├── dashboard.md
│   │   └── data-validation.md
│   ├── troubleshooting/               ← Troubleshooting
│   │   ├── redis-full-check-setup.md
│   │   └── common-issues.md
│   └── research/                      ← Deep dive (advanced)
│       ├── dragonfly-replica-protocol.md
│       ├── dragonfly-rdb-format.md
│       ├── dragonfly-stream-sync.md
│       └── fullsync-performance.md
├── api/
│   └── dashboard-api.md
└── archive/                           ← 归档旧文档（可选）
    ├── Phase-1.md ... Phase-7.md
    ├── fix-rdb-completion-race-condition.md
    ├── issue-type-assertion-performance.md
    └── Dragonfly-v1.36.0-Bug-Workaround.md
```

---

## 📋 执行步骤

### Step 1: 删除过时文档

```bash
# 删除已过时的文档
rm docs/zh/Dragonfly-v1.36.0-Bug-Workaround.md  # 官方已修复
rm docs/zh/architecture.md                      # 与 architecture/overview.md 重复
rm docs/en/architecture.md

# 归档 Phase 文档（如果需要保留）
mkdir -p docs/archive/development-logs
mv docs/zh/Phase-*.md docs/archive/development-logs/
mv docs/en/Phase-*.md docs/archive/development-logs/

# 或者直接删除
# rm docs/zh/Phase-*.md docs/en/Phase-*.md
```

### Step 2: 合并重复内容

```bash
# 合并 principle_and_implementation 到 research
mv docs/principle_and_implementation/*.md docs/zh/research/

# 删除空目录
rmdir docs/principle_and_implementation
```

### Step 3: 重组文档分类

```bash
# 创建新目录
mkdir -p docs/zh/guides
mkdir -p docs/zh/troubleshooting
mkdir -p docs/en/guides
mkdir -p docs/en/troubleshooting

# 移动文档到合适的目录
# Guides
mv docs/zh/dashboard.md docs/zh/guides/
mv docs/zh/data-validation.md docs/zh/guides/
mv docs/en/dashboard.md docs/en/guides/
mv docs/en/data-validation.md docs/en/guides/

# Troubleshooting
mv docs/zh/redis-full-check-setup.md docs/zh/troubleshooting/
mv docs/en/redis-full-check-setup.md docs/en/troubleshooting/

# 归档或删除
mv docs/zh/fix-rdb-completion-race-condition.md docs/archive/
mv docs/zh/issue-type-assertion-performance.md docs/archive/
mv docs/en/fix-rdb-completion-race-condition.md docs/archive/
mv docs/en/issue-type-assertion-performance.md docs/archive/
```

### Step 4: 清理 SVG 文件

```bash
cd docs/images/architecture

# 删除冗余的默认版本
rm replication-protocol.svg      # 与 -zh.svg 相同
rm state-machine-diagram.svg     # 与 -zh.svg 相同

# 重命名现有文件以保持一致性
mv cluster-routing.svg cluster-routing-zh.svg
mv data-pipeline.svg data-pipeline-zh.svg
mv multi-flow.svg multi-flow-zh.svg
mv RDB+Journal-data-flow.svg overview-zh.svg

# 创建英文版本占位符（稍后需要重新生成）
# TODO: 为这些图创建英文版本
```

### Step 5: 更新文档引用

```bash
# 更新所有 Markdown 文件中的链接
find docs -name "*.md" -exec sed -i '' \
  's|](../architecture\.md)|](../architecture/overview.md)|g' {} \;

# 更新 SVG 引用
find docs -name "*.md" -exec sed -i '' \
  's|replication-protocol\.svg|replication-protocol-zh.svg|g' {} \;

find docs -name "*.md" -exec sed -i '' \
  's|state-machine-diagram\.svg|state-machine-diagram-zh.svg|g' {} \;
```

### Step 6: 创建文档索引

**创建 `docs/README.md`**：
```markdown
# df2redis Documentation

## 📚 Documentation Structure

### [中文文档 (Chinese)](zh/README.md)
- [架构设计](zh/architecture/)
- [使用指南](zh/guides/)
- [故障排查](zh/troubleshooting/)
- [深入研究](zh/research/)

### [English Documentation](en/README.md)
- [Architecture](en/architecture/)
- [User Guides](en/guides/)
- [Troubleshooting](en/troubleshooting/)
- [Research](en/research/)

### API Reference
- [Dashboard API](api/dashboard-api.md)

### Diagrams
- [Diagram Sources](diagrams/) - Mermaid source files
```

---

## ✅ 预期效果

### 重组后的优势

1. **清晰的文档分类**
   - `architecture/` - 架构设计（面向开发者）
   - `guides/` - 使用指南（面向用户）
   - `troubleshooting/` - 故障排查（面向运维）
   - `research/` - 深入研究（面向高级用户）

2. **消除重复**
   - 删除重复的 `architecture.md`
   - 合并 `principle_and_implementation/` 到 `research/`
   - 归档过时的 Phase 开发日志

3. **一致的命名**
   - 所有 SVG 文件都有 `-zh` 和 `-en` 后缀
   - 文档路径结构对称（中英文）

4. **易于维护**
   - 每个文档有明确的目的和受众
   - 易于查找和更新
   - 归档目录保留历史但不干扰主文档

---

## 🚨 注意事项

### 重要：执行前备份

```bash
# 备份整个 docs 目录
cp -r docs docs_backup_$(date +%Y%m%d)

# 或创建 Git 分支
git checkout -b docs-reorganization
```

### 需要手动处理的任务

1. **翻译英文版本**
   - Phase 文档虽然要归档，但可以提炼关键内容到 architecture 文档
   - 确保英文 architecture 文档完整

2. **创建缺失的英文 SVG**
   - `cluster-routing-en.svg`
   - `data-pipeline-en.svg`
   - `multi-flow-en.svg`
   - `overview-en.svg`

3. **更新 README 引用**
   - 主 `README.md` 中的文档链接
   - `examples/` 中的配置注释

---

## 📊 文件变更统计

### 删除
- Phase 文档: 14 个文件（或归档）
- 重复文档: 4 个文件
- 过时文档: 1 个文件
- 冗余 SVG: 2 个文件

### 移动
- 分类整理: ~8 个文件
- 合并目录: 4 个文件

### 创建
- 索引文档: 3 个文件 (docs/README.md, zh/README.md, en/README.md)
- 新目录: 4 个 (guides/, troubleshooting/ 中英文各 2 个)

---

## 🎯 执行建议

### 分阶段执行

**阶段 1: 清理（低风险）**
1. 删除确认过时的文档
2. 归档 Phase 开发日志
3. 创建 archive/ 目录

**阶段 2: 重组（中等风险）**
1. 创建新目录结构
2. 移动文档到新位置
3. 更新内部链接

**阶段 3: 优化（需要工作量）**
1. 创建文档索引
2. 补充缺失的英文内容
3. 生成缺失的英文 SVG

### 验证步骤

```bash
# 检查损坏的链接
find docs -name "*.md" -exec grep -H "\]\(" {} \; | \
  grep -v "http" | grep -v "^#" > links.txt

# 检查未引用的文件
find docs/images -name "*.svg" -o -name "*.png" | while read img; do
  filename=$(basename "$img")
  if ! grep -r "$filename" docs --include="*.md" > /dev/null; then
    echo "Unused: $img"
  fi
done
```

---

## ✨ 总结

这个重组方案将：
- ✅ 清理 40+ 过时/重复文件
- ✅ 建立清晰的 4 层文档结构
- ✅ 统一中英文文档对称性
- ✅ 消除 SVG 文件冗余
- ✅ 提供清晰的文档索引

**建议优先级**：阶段 1（立即执行）→ 阶段 2（本周）→ 阶段 3（渐进补充）
