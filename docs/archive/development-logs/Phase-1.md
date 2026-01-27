# Phase 1 – Dragonfly Handshake (English Companion)

[中文文档](../Phase-1.md)

This note summarizes the first milestone documented in Chinese: implementing the complete Dragonfly replication handshake for df2redis.

## Highlights

- Explain the multi-step handshake (PING → REPLCONF listening-port/ip/capa → DFLY-specific capability negotiation).
- Describe how FLOW counts, replication IDs, and Dragonfly protocol versions are parsed from the `REPLCONF capa dragonfly` reply.
- Outline error handling expectations (fallbacks for legacy Redis responses, validation of array lengths, etc.).

## Implementation Checklist

1. Establish a main TCP connection and enable TCP keepalive.
2. Perform each REPLCONF step sequentially, logging detailed progress for operators.
3. Parse the Dragonfly-specific response to populate `MasterInfo` and determine how many FLOW sockets to open.
4. Build individual FLOW connections and register them via `DFLY FLOW` commands while capturing EOF tokens.

Refer to the Chinese document for diagrams, timing charts, and troubleshooting notes.
