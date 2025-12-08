# Phase 5 – Implementing Complex RDB Decoders

[中文文档](../Phase-5.md)

Phase 5 documents the effort to support Dragonfly/Redis complex RDB encodings beyond simple strings.

## Highlights

- Hash/list/set/zset decoders for ziplist, listpack, intset, QuickList 2.0, and Dragonfly type 18 listpacks.
- Helper parsers for listpack/ziplist structures, including backlen handling and integer encodings.
- Error handling for unsupported encodings to surface actionable feedback to operators.
- Mapping between decoded Go structs (`HashValue`, `ListValue`, etc.) and the replay logic that writes them into Redis.

## Checklist

1. Expand `RDBParser` with type-specific parsers plus helper utilities (`readDouble`, listpack parser, intset parser).
2. Validate new decoders with sample payloads from Dragonfly and Redis test suites.
3. Wire the new value types into `writeRDBEntry` so snapshot ingestion supports every structure seen in production.
4. Keep debug logging around to troubleshoot field-level parsing issues.

Full byte diagrams and timeline notes remain in the Chinese version.
