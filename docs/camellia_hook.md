# Camellia Meta Hook 集成指引

本项目在 `migrate` 流程中会生成 `hook.json`，内容包含：

```json
{
  "script_sha": "...",
  "script_path": "./lua/meta_cas.lua",
  "meta_pattern": "meta:{%s}",
  "baseline_key": "MIGRATE_BASE_TS",
  "updated_at": 1710000000
}
```

Camellia 侧需要在双写路径读取该文件，并按以下逻辑执行：

1. **加载脚本**：
   - 若 `script_sha` 缺失或 `SCRIPT EXISTS` 返回 0，则使用 `SCRIPT LOAD` 重新载入 `script_path` 指定的 Lua。
2. **执行写入**：
   - 在将命令写到 Redis 备端之前，通过 `EVALSHA script_sha 2 <data-key> <meta-key> <timestamp>` 更新 `meta:{key}`，其中 `meta-key` 可使用 `meta_pattern` 格式化得到。
   - 仅在 Lua 返回 1 时继续写入旧值（CAS 逻辑保证不覆盖新写）。
3. **保持最新**：
   - 监听 `hook.json` 的更新时间（例如使用文件监听或定时刷新），确保脚本/配置变更后及时生效。

> **提示**：Camellia 官方提供了自定义 `CommandInterceptor` 与 `KvWriteOpHook` 扩展点，可在对应位置读取 `hook.json` 并执行上述逻辑。推荐将文件路径设为只写目录（默认 `state/wal/hook.json`），以便运维侧查看。

## 样例伪代码

```java
HookMeta meta = HookMetaLoader.load("state/wal/hook.json");
String metaKey = String.format(meta.getPattern(), key);
Object result = redisClient.evalsha(meta.getSha(), Arrays.asList(key, metaKey), Arrays.asList(ts));
if (Objects.equals(result, Long.valueOf(1))) {
    // CAS 通过，继续把真实命令写入 Redis
}
```

如需更多实现细节，可参考 Camellia 插件机制：
- `camellia-redis-proxy` 模块下的 `CommandInterceptor`/`CommandActionPolicy`
- `camellia-redis-base` 中的双写样例

完成集成后，即可实现框架内的 “双写 + CAS meta 更新” 闭环。

> df2redis 的 `sync` 阶段会持续轮询 `wal/status.json`，当 backlog 清零并通过定期采样校验后会在 `status.json` 中写入 `sync-ready` 提示。请按提示完成人工流量切换，然后在 df2redis 终端按 `Ctrl+C`，流程会进入清理阶段并停止 Camellia。
