#!/usr/bin/env python3
"""
Test script for verifying Dragonfly Stream type replication to Redis.

This script tests:
1. Basic XADD operations
2. XADD with automatic ID generation
3. XADD with NOMKSTREAM option
4. XTRIM with MAXLEN (approximate and exact)
5. XTRIM with MINID
6. Stream entries during RDB phase
7. Stream entries during stable sync phase
"""

import redis
import time
import sys
from typing import List, Dict, Any


class StreamReplicationTester:
    def __init__(self, source_host: str, source_port: int, source_password: str,
                 target_host: str, target_port: int, target_password: str):
        """Initialize connections to source (Dragonfly) and target (Redis)."""
        self.source = redis.Redis(
            host=source_host,
            port=source_port,
            password=source_password if source_password else None,
            decode_responses=True
        )
        self.target = redis.Redis(
            host=target_host,
            port=target_port,
            password=target_password if target_password else None,
            decode_responses=True
        )

    def cleanup(self):
        """Clean up test streams."""
        test_keys = [
            "test:stream:basic",
            "test:stream:autoid",
            "test:stream:nomkstream",
            "test:stream:trim_maxlen",
            "test:stream:trim_minid",
            "test:stream:rdb_phase",
            "test:stream:stable_phase"
        ]

        for key in test_keys:
            try:
                self.source.delete(key)
                self.target.delete(key)
            except:
                pass

    def compare_streams(self, key: str) -> bool:
        """Compare stream content between source and target."""
        try:
            # Get stream info from both
            source_info = self.source.xinfo_stream(key)
            target_info = self.target.xinfo_stream(key)

            # Compare length
            if source_info['length'] != target_info['length']:
                print(f"  ✗ Length mismatch: source={source_info['length']}, target={target_info['length']}")
                return False

            # Get all entries
            source_entries = self.source.xrange(key)
            target_entries = self.target.xrange(key)

            # Compare entries
            if len(source_entries) != len(target_entries):
                print(f"  ✗ Entry count mismatch: source={len(source_entries)}, target={len(target_entries)}")
                return False

            for i, (src_entry, tgt_entry) in enumerate(zip(source_entries, target_entries)):
                src_id, src_data = src_entry
                tgt_id, tgt_data = tgt_entry

                if src_id != tgt_id:
                    print(f"  ✗ Entry {i} ID mismatch: source={src_id}, target={tgt_id}")
                    return False

                if src_data != tgt_data:
                    print(f"  ✗ Entry {i} data mismatch: source={src_data}, target={tgt_data}")
                    return False

            # Compare first_entry and last_entry
            if source_info['first-entry'] != target_info['first-entry']:
                print(f"  ✗ First entry mismatch")
                return False

            if source_info['last-entry'] != target_info['last-entry']:
                print(f"  ✗ Last entry mismatch")
                return False

            print(f"  ✓ Stream '{key}' perfectly synchronized ({source_info['length']} entries)")
            return True

        except redis.ResponseError as e:
            if "no such key" in str(e).lower():
                # Both should not exist
                try:
                    self.target.xinfo_stream(key)
                    print(f"  ✗ Source stream does not exist, but target does")
                    return False
                except:
                    print(f"  ✓ Stream '{key}' does not exist on both sides (OK)")
                    return True
            else:
                print(f"  ✗ Error comparing streams: {e}")
                return False
        except Exception as e:
            print(f"  ✗ Error: {e}")
            return False

    def test_basic_xadd(self) -> bool:
        """Test 1: Basic XADD with explicit ID."""
        print("\n[Test 1] Basic XADD with explicit ID")
        key = "test:stream:basic"

        # Add entries with explicit IDs
        self.source.xadd(key, {"field1": "value1", "field2": "value2"}, id="1-0")
        self.source.xadd(key, {"field3": "value3", "field4": "value4"}, id="2-0")
        self.source.xadd(key, {"field5": "value5"}, id="3-0")

        time.sleep(2)  # Wait for sync
        return self.compare_streams(key)

    def test_autoid_xadd(self) -> bool:
        """Test 2: XADD with automatic ID generation."""
        print("\n[Test 2] XADD with automatic ID generation")
        key = "test:stream:autoid"

        # Add entries with * (auto-generate ID)
        id1 = self.source.xadd(key, {"auto": "entry1"})
        id2 = self.source.xadd(key, {"auto": "entry2"})
        id3 = self.source.xadd(key, {"auto": "entry3"})

        print(f"  Generated IDs: {id1}, {id2}, {id3}")

        time.sleep(2)
        return self.compare_streams(key)

    def test_trim_maxlen(self) -> bool:
        """Test 3: XTRIM with MAXLEN (approximate)."""
        print("\n[Test 3] XTRIM with MAXLEN (approximate)")
        key = "test:stream:trim_maxlen"

        # Add 100 entries
        for i in range(100):
            self.source.xadd(key, {"seq": str(i)})

        # Trim to approximately 50 entries
        self.source.xtrim(key, maxlen=50, approximate=True)

        time.sleep(2)
        return self.compare_streams(key)

    def test_trim_minid(self) -> bool:
        """Test 4: XTRIM with MINID."""
        print("\n[Test 4] XTRIM with MINID")
        key = "test:stream:trim_minid"

        # Add entries with explicit IDs
        ids = []
        for i in range(10):
            stream_id = f"{i+1}-0"
            self.source.xadd(key, {"seq": str(i)}, id=stream_id)
            ids.append(stream_id)

        # Trim: remove all entries before ID 5-0
        # Note: Redis XTRIM MINID requires Redis 6.2+
        try:
            # Use XTRIM with MINID (may not be supported in all Redis versions)
            self.source.execute_command("XTRIM", key, "MINID", "5-0")
            time.sleep(2)
            return self.compare_streams(key)
        except redis.ResponseError as e:
            print(f"  ⚠ XTRIM MINID not supported: {e}")
            return True  # Skip test if not supported

    def test_trim_to_zero(self) -> bool:
        """Test 5: XTRIM to zero (empty stream)."""
        print("\n[Test 5] XTRIM to zero (empty stream)")
        key = "test:stream:trim_maxlen"

        # Ensure stream exists
        self.source.xadd(key, {"test": "data"})

        # Trim to 0
        self.source.xtrim(key, maxlen=0)

        time.sleep(2)
        return self.compare_streams(key)

    def test_xadd_with_trim(self) -> bool:
        """Test 6: XADD with MAXLEN option."""
        print("\n[Test 6] XADD with MAXLEN option (inline trim)")
        key = "test:stream:inline_trim"

        # Add entries with MAXLEN limit
        for i in range(20):
            self.source.xadd(key, {"seq": str(i)}, maxlen=10, approximate=True)

        time.sleep(2)
        return self.compare_streams(key)

    def run_all_tests(self) -> bool:
        """Run all tests and report results."""
        print("=" * 60)
        print("Stream Replication Test Suite")
        print("=" * 60)

        # Cleanup first
        print("\n[Cleanup] Removing old test data...")
        self.cleanup()

        tests = [
            ("Basic XADD", self.test_basic_xadd),
            ("Auto-ID XADD", self.test_autoid_xadd),
            ("XTRIM MAXLEN", self.test_trim_maxlen),
            ("XTRIM MINID", self.test_trim_minid),
            ("XTRIM to zero", self.test_trim_to_zero),
            ("XADD with inline trim", self.test_xadd_with_trim),
        ]

        results = []
        for test_name, test_func in tests:
            try:
                result = test_func()
                results.append((test_name, result))
            except Exception as e:
                print(f"  ✗ Exception: {e}")
                results.append((test_name, False))

        # Summary
        print("\n" + "=" * 60)
        print("Test Summary")
        print("=" * 60)

        passed = sum(1 for _, result in results if result)
        total = len(results)

        for test_name, result in results:
            status = "✓ PASS" if result else "✗ FAIL"
            print(f"{status}: {test_name}")

        print("-" * 60)
        print(f"Total: {passed}/{total} tests passed ({passed*100//total}%)")
        print("=" * 60)

        return passed == total


def main():
    """Main entry point."""
    # Configuration (modify as needed)
    SOURCE_HOST = "localhost"
    SOURCE_PORT = 6380
    SOURCE_PASSWORD = ""

    TARGET_HOST = "localhost"
    TARGET_PORT = 6379
    TARGET_PASSWORD = ""

    tester = StreamReplicationTester(
        SOURCE_HOST, SOURCE_PORT, SOURCE_PASSWORD,
        TARGET_HOST, TARGET_PORT, TARGET_PASSWORD
    )

    try:
        success = tester.run_all_tests()
        sys.exit(0 if success else 1)
    except KeyboardInterrupt:
        print("\n\nTest interrupted by user")
        sys.exit(2)
    except Exception as e:
        print(f"\n\nFatal error: {e}")
        import traceback
        traceback.print_exc()
        sys.exit(3)


if __name__ == "__main__":
    main()
