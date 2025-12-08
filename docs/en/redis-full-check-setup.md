# redis-full-check Installation Guide

[中文文档](../redis-full-check-setup.md)

This is a condensed translation of the Chinese installation guide for Alibaba's `redis-full-check`.

## Build from source (recommended)

```bash
git clone https://github.com/alibaba/RedisFullCheck.git
cd RedisFullCheck
make
cp bin/redis-full-check /path/to/df2redis/bin/
```

## Download a release (if available)

Check the upstream GitHub releases for prebuilt binaries, download the appropriate archive, and place the binary on your `PATH`.

## Use the helper script

The repository ships with `scripts/download-redis-full-check.sh` which sets up the folder structure and prints manual build instructions. Run it from the repo root and follow the prompts.

## Verification

After installation, run `redis-full-check -v` (or provide the absolute path configured through df2redis) to confirm that the binary launches.

The Chinese document lists additional troubleshooting Q&A and environment notes.
