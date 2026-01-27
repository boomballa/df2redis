# Data Consistency Validation

[中文文档](../../zh/guides/data-validation.md)

df2redis integrates [redis-full-check](https://github.com/alibaba/RedisFullCheck) so operators can validate Dragonfly → Redis migrations.

## What the Chinese doc covers

- CLI options exposed via `df2redis check` (mode, QPS, batch size, filters, log level, etc.).
- Recommended workflows (pre-migration baseline, incremental verification, sampling strategies).
- Example outputs highlighting mismatch summaries and sample keys.

## Key Steps

1. Install `redis-full-check` (see the dedicated setup guide) and ensure the binary is accessible.
2. Configure source/target addresses plus optional filters via the df2redis config file.
3. Run `df2redis check` and inspect the generated JSON + summary files under the configured result directory.

See the Chinese write-up for screenshot-like log samples and troubleshooting tips.
