#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
df2redis RDB Phase Data Sync Test Script

Purpose:
  Test data consistency during RDB phase by writing keys while RDB import is in progress,
  then verify all keys are correctly synchronized to target.

Usage:
  1. Start df2redis with: ./bin/df2redis replicate --config <config_path>
  2. Run this script in one of three ways:
     - Auto-detect: python3 scripts/test_rdb_phase_sync.py
       (Detects config from running df2redis process)
     - Explicit config: python3 scripts/test_rdb_phase_sync.py examples/replicate.sample.yaml
     - Default config: python3 scripts/test_rdb_phase_sync.py
       (Uses config.yaml in current directory)
  3. Script will wait for RDB phase, write test data, and verify results

Expected Result:
  After the fix, ALL test keys should sync successfully (0% data loss).
"""

import redis
import time
import yaml
import sys
import os
import subprocess
import re
from typing import Dict, Tuple, List, Optional

# Test configuration
TEST_PREFIX = "rdb_phase_test:"
TEST_KEYS_COUNT = 20  # Write 20 test keys during RDB phase

class Colors:
    """ANSI color codes for terminal output"""
    GREEN = '\033[92m'
    RED = '\033[91m'
    YELLOW = '\033[93m'
    BLUE = '\033[94m'
    BOLD = '\033[1m'
    END = '\033[0m'

def get_running_df2redis_config() -> Optional[str]:
    """Detect running df2redis process and extract config file path"""
    try:
        # Get all running processes
        result = subprocess.run(['ps', '-ef'], capture_output=True, text=True, timeout=5)
        if result.returncode != 0:
            return None

        # Search for df2redis process
        for line in result.stdout.splitlines():
            if 'df2redis' in line and 'replicate' in line and '--config' in line:
                # Extract config path using regex
                # Pattern: --config <path>
                match = re.search(r'--config\s+(\S+)', line)
                if match:
                    config_path = match.group(1)
                    print(f"{Colors.BLUE}🔍 Found running df2redis process{Colors.END}")
                    print(f"{Colors.BLUE}   Config file: {config_path}{Colors.END}")
                    return config_path

        return None
    except Exception as e:
        print(f"{Colors.YELLOW}⚠ Failed to detect running df2redis: {e}{Colors.END}")
        return None

def load_config(config_path: str = "config.yaml") -> Dict:
    """Load and parse df2redis config.yaml"""
    # First, try the provided path
    if os.path.exists(config_path):
        try:
            with open(config_path, 'r') as f:
                config = yaml.safe_load(f)
            print(f"{Colors.GREEN}✓ Loaded config from {config_path}{Colors.END}")
            return config
        except Exception as e:
            print(f"{Colors.RED}✗ Failed to load config: {e}{Colors.END}")
            sys.exit(1)

    # If default config not found, try to detect from running process
    print(f"{Colors.YELLOW}⚠ Config file not found: {config_path}{Colors.END}")
    print(f"{Colors.BLUE}🔍 Attempting to detect config from running df2redis process...{Colors.END}")

    detected_config = get_running_df2redis_config()
    if detected_config and os.path.exists(detected_config):
        try:
            with open(detected_config, 'r') as f:
                config = yaml.safe_load(f)
            print(f"{Colors.GREEN}✓ Loaded config from detected path: {detected_config}{Colors.END}")
            return config
        except Exception as e:
            print(f"{Colors.RED}✗ Failed to load detected config: {e}{Colors.END}")
            sys.exit(1)

    # No config found
    print(f"{Colors.RED}✗ Could not find config file{Colors.END}")
    print(f"{Colors.YELLOW}💡 Solutions:{Colors.END}")
    print(f"   1. Run this script from df2redis root directory")
    print(f"   2. Or start df2redis first: ./bin/df2redis replicate --config <path>{Colors.END}")
    print(f"   3. Or specify config path: python3 scripts/test_rdb_phase_sync.py <config_path>{Colors.END}")
    sys.exit(1)

def connect_redis(label: str, host: str, port: int, password: str = "", db: int = 0) -> redis.Redis:
    """Create Redis/Dragonfly connection"""
    try:
        r = redis.Redis(
            host=host,
            port=port,
            password=password,
            db=db,
            decode_responses=True,
            socket_timeout=10
        )
        r.ping()
        print(f"{Colors.GREEN}✓ Connected to {label}: {host}:{port}{Colors.END}")
        return r
    except redis.AuthenticationError:
        print(f"{Colors.RED}✗ Failed to connect to {label}: Authentication failed{Colors.END}")
        sys.exit(1)
    except redis.ConnectionError:
        print(f"{Colors.RED}✗ Failed to connect to {label}: Connection refused{Colors.END}")
        sys.exit(1)
    except Exception as e:
        print(f"{Colors.RED}✗ Failed to connect to {label}: {e}{Colors.END}")
        sys.exit(1)

def clean_test_keys(r: redis.Redis, label: str):
    """Clean up test keys using standard KEYS command"""
    try:
        keys = r.keys(f"{TEST_PREFIX}*")
        if keys:
            r.delete(*keys)
            print(f"{Colors.GREEN}✓ Cleaned {len(keys)} test keys from {label}{Colors.END}")
        else:
            print(f"{Colors.GREEN}✓ No test keys found in {label}{Colors.END}")
    except Exception as e:
        print(f"{Colors.RED}✗ Failed to clean test keys from {label}: {e}{Colors.END}")

def wait_for_user_start():
    """Wait for user to start df2redis"""
    print(f"\n{Colors.BOLD}{'='*70}{Colors.END}")
    print(f"{Colors.BOLD}📋 Test Preparation Steps:{Colors.END}")
    print(f"{Colors.YELLOW}   1. Make sure source Dragonfly has significant data (e.g., 1M+ keys){Colors.END}")
    print(f"{Colors.YELLOW}   2. Start df2redis in another terminal:{Colors.END}")
    print(f"{Colors.BLUE}      ./bin/df2redis-mac replicate --config config.yaml{Colors.END}")
    print(f"{Colors.YELLOW}   3. Wait for RDB import to start (you'll see FLOW logs){Colors.END}")
    print(f"{Colors.YELLOW}   4. Then press Enter here to start writing test data{Colors.END}")
    print(f"{Colors.BOLD}{'='*70}{Colors.END}\n")

    input(f"{Colors.BOLD}Press Enter when df2redis RDB phase has started...{Colors.END} ")
    print(f"\n{Colors.GREEN}✓ Starting test data generation...{Colors.END}\n")

def write_test_data(source_r: redis.Redis) -> Tuple[bool, List[str]]:
    """Write diverse test keys to source during RDB phase"""
    print(f"{Colors.BOLD}📝 Writing {TEST_KEYS_COUNT} test keys to source Dragonfly...{Colors.END}")

    test_keys = []

    try:
        for i in range(TEST_KEYS_COUNT):
            key = f"{TEST_PREFIX}key_{i:03d}"

            # Vary the data type for diversity
            data_type = i % 6

            if data_type == 0:
                # String
                value = f"test_value_{i}_timestamp_{int(time.time())}"
                source_r.set(key, value, ex=600)  # 10 min TTL
                test_keys.append((key, "string", value))

            elif data_type == 1:
                # Hash
                source_r.hset(key, mapping={
                    "field1": f"value_{i}_1",
                    "field2": f"value_{i}_2",
                    "counter": i
                })
                test_keys.append((key, "hash", None))

            elif data_type == 2:
                # List
                source_r.lpush(key, f"item_{i}_1", f"item_{i}_2", f"item_{i}_3")
                test_keys.append((key, "list", None))

            elif data_type == 3:
                # Set
                source_r.sadd(key, f"member_{i}_1", f"member_{i}_2", f"member_{i}_3")
                test_keys.append((key, "set", None))

            elif data_type == 4:
                # ZSet
                source_r.zadd(key, {
                    f"member_{i}_1": float(i * 10),
                    f"member_{i}_2": float(i * 10 + 5),
                    f"member_{i}_3": float(i * 10 + 10)
                })
                test_keys.append((key, "zset", None))

            else:
                # String with special characters
                value = f"special_测试_{i}_🔥_{int(time.time())}"
                source_r.set(key, value)
                test_keys.append((key, "string", value))

            # Print progress
            if (i + 1) % 5 == 0:
                print(f"{Colors.BLUE}  → Written {i + 1}/{TEST_KEYS_COUNT} keys...{Colors.END}")

            # Small delay to spread writes across time window
            time.sleep(0.1)

        print(f"{Colors.GREEN}✓ Successfully wrote {TEST_KEYS_COUNT} test keys{Colors.END}")
        return True, test_keys

    except Exception as e:
        print(f"{Colors.RED}✗ Failed to write test data: {e}{Colors.END}")
        return False, []

def verify_test_data(source_r: redis.Redis, target_r: redis.Redis, test_keys: List[Tuple]) -> Tuple[int, int, List[str]]:
    """Verify all test keys are correctly synced"""
    print(f"\n{Colors.BOLD}🔍 Verifying test data synchronization...{Colors.END}")

    success_count = 0
    fail_count = 0
    fail_details = []

    for key, key_type, expected_value in test_keys:
        try:
            # Check if key exists in target
            if not target_r.exists(key):
                fail_count += 1
                fail_details.append(f"{key}: Missing in target")
                print(f"{Colors.RED}✗ {key}: Missing in target{Colors.END}")
                continue

            # Verify type matches
            src_type = source_r.type(key)
            dst_type = target_r.type(key)
            if src_type != dst_type:
                fail_count += 1
                fail_details.append(f"{key}: Type mismatch (src={src_type}, dst={dst_type})")
                print(f"{Colors.RED}✗ {key}: Type mismatch{Colors.END}")
                continue

            # Type-specific verification
            if key_type == "string":
                src_val = source_r.get(key)
                dst_val = target_r.get(key)
                if src_val != dst_val:
                    fail_count += 1
                    fail_details.append(f"{key}: Value mismatch")
                    print(f"{Colors.RED}✗ {key}: Value mismatch{Colors.END}")
                    continue

            elif key_type == "hash":
                src_hash = source_r.hgetall(key)
                dst_hash = target_r.hgetall(key)
                if src_hash != dst_hash:
                    fail_count += 1
                    fail_details.append(f"{key}: Hash mismatch")
                    print(f"{Colors.RED}✗ {key}: Hash mismatch{Colors.END}")
                    continue

            elif key_type == "list":
                src_list = source_r.lrange(key, 0, -1)
                dst_list = target_r.lrange(key, 0, -1)
                if src_list != dst_list:
                    fail_count += 1
                    fail_details.append(f"{key}: List mismatch")
                    print(f"{Colors.RED}✗ {key}: List mismatch{Colors.END}")
                    continue

            elif key_type == "set":
                src_set = source_r.smembers(key)
                dst_set = target_r.smembers(key)
                if src_set != dst_set:
                    fail_count += 1
                    fail_details.append(f"{key}: Set mismatch")
                    print(f"{Colors.RED}✗ {key}: Set mismatch{Colors.END}")
                    continue

            elif key_type == "zset":
                src_zset = source_r.zrange(key, 0, -1, withscores=True)
                dst_zset = target_r.zrange(key, 0, -1, withscores=True)
                if src_zset != dst_zset:
                    fail_count += 1
                    fail_details.append(f"{key}: ZSet mismatch")
                    print(f"{Colors.RED}✗ {key}: ZSet mismatch{Colors.END}")
                    continue

            # If we reach here, verification passed
            success_count += 1
            print(f"{Colors.GREEN}✓ {key}: Verified{Colors.END}")

        except Exception as e:
            fail_count += 1
            fail_details.append(f"{key}: Verification error - {e}")
            print(f"{Colors.RED}✗ {key}: Verification error{Colors.END}")

    return success_count, fail_count, fail_details

def main():
    """Main test flow"""
    print(f"\n{Colors.BOLD}{'='*70}{Colors.END}")
    print(f"{Colors.BOLD}🧪 df2redis RDB Phase Data Sync Test{Colors.END}")
    print(f"{Colors.BOLD}{'='*70}{Colors.END}\n")

    # Step 1: Load configuration (support command line arg)
    config_path = sys.argv[1] if len(sys.argv) > 1 else "config.yaml"
    if len(sys.argv) > 1:
        print(f"{Colors.BLUE}📋 Using config from command line: {config_path}{Colors.END}\n")
    config = load_config(config_path)

    # Extract source and target info
    source_host = config['source']['addr'].split(':')[0]
    source_port = int(config['source']['addr'].split(':')[1])
    source_password = config['source'].get('password', '')

    # Extract target info
    target_config = config['target']
    target_host = target_config['addr'].split(':')[0]
    target_port = int(target_config['addr'].split(':')[1])
    target_password = target_config.get('password', '')

    print(f"{Colors.BLUE}Source: {source_host}:{source_port}{Colors.END}")
    print(f"{Colors.BLUE}Target: {target_host}:{target_port} ({target_config['type']}){Colors.END}\n")

    # Step 2: Connect to source and target
    source_r = connect_redis("Source (Dragonfly)", source_host, source_port, source_password)
    target_r = connect_redis("Target (Redis)", target_host, target_port, target_password)

    # Step 3: Clean up any existing test keys
    print()
    clean_test_keys(source_r, "Source")
    clean_test_keys(target_r, "Target")

    # Step 4: Wait for user to start df2redis RDB import
    wait_for_user_start()

    # Step 5: Write test data during RDB phase
    success, test_keys = write_test_data(source_r)
    if not success:
        print(f"\n{Colors.RED}✗ Test failed: Could not write test data{Colors.END}")
        sys.exit(1)

    # Step 6: Wait for synchronization to complete
    # We need to wait long enough for:
    # 1. df2redis to finish RDB import
    # 2. All journal blobs to be processed
    # 3. Data to be written to target
    wait_seconds = 120
    print(f"\n{Colors.YELLOW}⏳ Waiting {wait_seconds} seconds for synchronization to complete...{Colors.END}")
    print(f"{Colors.YELLOW}   (This allows df2redis to finish RDB phase and process journal blobs){Colors.END}")

    for i in range(wait_seconds // 10):
        time.sleep(10)
        remaining = wait_seconds - (i + 1) * 10
        print(f"{Colors.BLUE}  → {remaining} seconds remaining...{Colors.END}")

    # Step 7: Verify all test keys
    success_count, fail_count, fail_details = verify_test_data(source_r, target_r, test_keys)

    # Step 8: Print final results
    print(f"\n{Colors.BOLD}{'='*70}{Colors.END}")
    print(f"{Colors.BOLD}📊 Test Results Summary{Colors.END}")
    print(f"{Colors.BOLD}{'='*70}{Colors.END}")
    print(f"Total test keys: {len(test_keys)}")
    print(f"{Colors.GREEN}✓ Successfully synced: {success_count}{Colors.END}")
    print(f"{Colors.RED}✗ Failed to sync: {fail_count}{Colors.END}")

    if fail_count > 0:
        data_loss_rate = (fail_count / len(test_keys)) * 100
        print(f"\n{Colors.RED}❌ Data Loss Rate: {data_loss_rate:.1f}%{Colors.END}")
        print(f"\n{Colors.RED}Failed keys:{Colors.END}")
        for detail in fail_details:
            print(f"  • {detail}")

        print(f"\n{Colors.YELLOW}💡 Troubleshooting:{Colors.END}")
        print(f"  1. Check df2redis logs for errors")
        print(f"  2. Verify df2redis completed RDB import phase")
        print(f"  3. Check if journal blobs were processed")
        print(f"  4. Manually verify with redis-cli:")
        print(f"     Source: redis-cli -h {source_host} -p {source_port} keys {TEST_PREFIX}*")
        print(f"     Target: redis-cli -h {target_host} -p {target_port} keys {TEST_PREFIX}*")
    else:
        print(f"\n{Colors.GREEN}🎉 SUCCESS! All test keys synchronized correctly (0% data loss){Colors.END}")
        print(f"{Colors.GREEN}✓ RDB phase data consistency verified{Colors.END}")

    print(f"\n{Colors.BOLD}{'='*70}{Colors.END}\n")

    # Step 9: Optional cleanup
    cleanup = input(f"{Colors.YELLOW}Do you want to clean up test keys? [y/N]: {Colors.END}").lower()
    if cleanup == 'y':
        clean_test_keys(source_r, "Source")
        clean_test_keys(target_r, "Target")
        print(f"{Colors.GREEN}✓ Cleanup complete{Colors.END}\n")

    # Exit with proper code
    sys.exit(0 if fail_count == 0 else 1)

if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        print(f"\n\n{Colors.YELLOW}⚠ Test interrupted by user{Colors.END}")
        sys.exit(1)
    except Exception as e:
        print(f"\n{Colors.RED}✗ Unexpected error: {e}{Colors.END}")
        import traceback
        traceback.print_exc()
        sys.exit(1)
