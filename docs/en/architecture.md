# Architecture – Dragonfly → Redis Replication

[中文文档](../architecture.md)

This document provides an English snapshot of the overall architecture described in Chinese.

## Components

- **Dragonfly source** – exposes the replication protocol (`DFLY FLOW`, journal stream) and sends both RDB snapshots and incremental updates.
- **df2redis replicator** – main process running handshake, FLOW coordination, RDB parser, journal replay, checkpoint saver, and logging.
- **Redis/Redis Cluster target** – receives snapshot writes and journal commands through the built-in cluster client.
- **Supporting tools** – redis-shake (legacy migrate pipeline), redis-full-check, dashboard/state store.

## Data Flow

1. Main connection negotiates replication metadata and triggers RDB transfer.
2. FLOW sockets stream snapshot data; df2redis parses and writes to Redis.
3. After `STARTSTABLE`, Dragonfly switches to journal streaming for incremental sync.
4. Replay logic routes commands per slot, handles conflicts/TTL, and updates checkpoints.

Consult the Chinese document for sequence diagrams and sizing guidelines.
