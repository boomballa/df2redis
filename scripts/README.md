# df2redis Test Scripts

## Overview

This directory contains test scripts for validating df2redis functionality.

## Installation

All test scripts require Python 3.7+ and the dependencies listed in `requirements.txt`.

```bash
# Install dependencies
pip3 install -r scripts/requirements.txt

# Or using a virtual environment (recommended)
python3 -m venv venv
source venv/bin/activate  # On Windows: venv\Scripts\activate
pip install -r scripts/requirements.txt
```

## test_rdb_phase_sync.py

**Purpose**: Test data consistency during RDB phase by writing keys while RDB import is in progress.

### What This Test Does

1. Reads your `config.yaml` to get source and target Redis configuration
2. Waits for you to start df2redis RDB import
3. Writes 20 test keys of different types (String, Hash, List, Set, ZSet) to source Dragonfly
4. Waits 120 seconds for synchronization to complete
5. Verifies all test keys are correctly synchronized to target
6. Reports success rate and data loss percentage

### Why This Test Is Important

This test validates the **RDB completion race condition fix**:
- Before fix: Keys written during RDB phase had 50% data loss
- After fix: All keys should sync successfully (0% data loss)

### Prerequisites

1. Install Python dependencies (see [Installation](#installation) above)
2. Make sure source Dragonfly has significant data (1M+ keys) - this ensures RDB phase takes long enough to write test data
3. Have a valid config.yaml in the project root or specify config path explicitly

### Usage

**Step 1**: Start df2redis in one terminal

```bash
# From project root
./bin/df2redis replicate --config examples/replicate.sample.yaml
```

**Step 2**: Wait for RDB import to start (you'll see FLOW logs like):

```
[FLOW-0] Starting to parse RDB data...
[FLOW-1] Starting to parse RDB data...
...
```

**Step 3**: In another terminal, run the test script

The script supports multiple ways to specify config:

```bash
# Option 1: Auto-detect from running df2redis process (recommended)
python3 scripts/test_rdb_phase_sync.py

# Option 2: Explicitly specify config file
python3 scripts/test_rdb_phase_sync.py examples/replicate.sample.yaml

# Option 3: Use default config.yaml in current directory
cd /path/to/df2redis
python3 scripts/test_rdb_phase_sync.py
```

**How config detection works:**
- First tries the default `config.yaml` in current directory
- If not found, automatically detects running df2redis process via `ps -ef`
- Extracts `--config` argument from the process command line
- Uses that config file

**Step 4**: When prompted, press Enter to start writing test data

```
Press Enter when df2redis RDB phase has started...
```

**Step 5**: Wait for results

The script will:
- Write 20 test keys during RDB phase
- Wait 120 seconds for sync to complete
- Verify all keys and report results

### Expected Output (After Fix)

```
🧪 df2redis RDB Phase Data Sync Test
======================================================================

✓ Connected to Source (Dragonfly): localhost:16379
Target: localhost:6379 (redis-cluster)

✓ Connected to Source (Dragonfly): localhost:16379
✓ Connected to Target (Redis): localhost:6379

Press Enter when df2redis RDB phase has started...

📝 Writing 20 test keys to source Dragonfly...
  → Written 5/20 keys...
  → Written 10/20 keys...
  → Written 15/20 keys...
  → Written 20/20 keys...
✓ Successfully wrote 20 test keys

⏳ Waiting 120 seconds for synchronization to complete...

🔍 Verifying test data synchronization...
✓ rdb_phase_test:key_000: Verified
✓ rdb_phase_test:key_001: Verified
...
✓ rdb_phase_test:key_019: Verified

======================================================================
📊 Test Results Summary
======================================================================
Total test keys: 20
✓ Successfully synced: 20
✗ Failed to sync: 0

🎉 SUCCESS! All test keys synchronized correctly (0% data loss)
✓ RDB phase data consistency verified
```

### Interpreting Results

**✅ Success (0% data loss)**
- Fix is working correctly
- All keys written during RDB phase are synchronized

**❌ Failure (>0% data loss)**
- Fix may not be working
- Check df2redis logs for errors
- Verify FULLSYNC_END handling and barrier synchronization

### Troubleshooting

#### Issue: "Config file not found"

**Solution 1**: Let script auto-detect from running df2redis
```bash
# Start df2redis first
./bin/df2redis replicate --config examples/replicate.sample.yaml

# Then run test script (will auto-detect config)
python3 scripts/test_rdb_phase_sync.py
```

**Solution 2**: Specify config explicitly
```bash
python3 scripts/test_rdb_phase_sync.py examples/replicate.sample.yaml
```

**Solution 3**: Run from project root with default config.yaml
```bash
cd /path/to/df2redis
python3 scripts/test_rdb_phase_sync.py
```

#### Issue: "Failed to connect to Source"
- Verify source Dragonfly is running
- Check config.yaml has correct source address/password
- Verify source can be accessed: `redis-cli -h <source-host> -p <source-port> -a <password> ping`

#### Issue: "Failed to connect to Target"
- Verify target Redis is running
- Check config.yaml has correct target address/password
- Verify target can be accessed: `redis-cli -h <target-host> -p <target-port> -a <password> ping`

#### Issue: High data loss rate
- Check df2redis logs for FULLSYNC_END handling
- Verify barrier synchronization is working
- Check for errors in journal blob processing
- Manually verify keys:
  ```bash
  # Source (Dragonfly)
  redis-cli -h <source-host> -p <source-port> -a <password> keys rdb_phase_test:*

  # Target (Redis)
  redis-cli -h <target-host> -p <target-port> -a <password> keys rdb_phase_test:*
  ```

### Cleanup

The script will prompt you to clean up test keys after verification:

```
Do you want to clean up test keys? [y/N]: y
```

Or manually clean up:

```bash
# Source (Dragonfly)
redis-cli -h <source-host> -p <source-port> -a <password> keys rdb_phase_test:* | xargs redis-cli -h <source-host> -p <source-port> -a <password> del

# Target (Redis)
redis-cli -h <target-host> -p <target-port> -a <password> keys rdb_phase_test:* | xargs redis-cli -h <target-host> -p <target-port> -a <password> del
```

## test_stream_replication.py

**Purpose**: Comprehensive test suite for validating Stream data type replication from Dragonfly to Redis.

### What This Test Does

Tests all critical Stream operations:
1. **Basic XADD** with explicit IDs
2. **Auto-ID XADD** with `*` wildcard
3. **XTRIM MAXLEN** with approximate trimming
4. **XTRIM MINID** with exact boundaries
5. **XTRIM to zero** (empty stream)
6. **XADD with inline trim** (MAXLEN option)

### Prerequisites

1. Install Python dependencies (see [Installation](#installation) above)
2. Ensure df2redis is running and actively replicating
3. Both source and target must be accessible

### Usage

```bash
# 1. Start df2redis replication
./bin/df2redis replicate --config examples/replicate.sample.yaml

# 2. Run Stream tests (modify host/port/password in the script if needed)
python3 scripts/test_stream_replication.py
```

### Configuration

Edit the script to match your environment:

```python
# Modify these values to match your environment
SOURCE_HOST = "localhost"
SOURCE_PORT = 16379
SOURCE_PASSWORD = ""

TARGET_HOST = "localhost"
TARGET_PORT = 6379
TARGET_PASSWORD = ""
```

### Expected Output

```
============================================================
Stream Replication Test Suite
============================================================

[Cleanup] Removing old test data...

[Test 1] Basic XADD with explicit ID
  ✓ Stream 'test:stream:basic' perfectly synchronized (3 entries)

[Test 2] XADD with automatic ID generation
  Generated IDs: 1735123456789-0, 1735123456790-0, 1735123456791-0
  ✓ Stream 'test:stream:autoid' perfectly synchronized (3 entries)

...

============================================================
Test Summary
============================================================
✓ PASS: Basic XADD
✓ PASS: Auto-ID XADD
✓ PASS: XTRIM MAXLEN
✓ PASS: XTRIM MINID
✓ PASS: XTRIM to zero
✓ PASS: XADD with inline trim
------------------------------------------------------------
Total: 6/6 tests passed (100%)
============================================================
```

### Why This Test Is Important

Validates that Dragonfly's Stream-specific journal rewrites are correctly replicated:
- **XADD**: Ensures actual generated IDs are used (not command arguments)
- **XTRIM**: Verifies MINID/MAXLEN exact boundaries after approximate trimming
- **Empty streams**: Confirms MAXLEN 0 handling

## manual_test_all_types.sh

**Purpose**: Simple one-command test for all 5 Redis data types during RDB phase.

### What This Test Does

1. Cleans up old test data
2. Waits for df2redis to initialize (3 seconds)
3. Writes 20 test keys (4 of each type: String, Hash, List, Set, ZSet)
4. Waits 120 seconds for synchronization
5. Verifies all keys and reports results

### Prerequisites

1. `redis-cli` must be installed and in PATH
2. df2redis should be running
3. Source and target must be accessible

### Usage

```bash
# 1. Edit script configuration if needed
vim scripts/manual_test_all_types.sh
# Modify: SOURCE_HOST, SOURCE_PORT, TARGET_HOST, TARGET_PORT, TARGET_PASS

# 2. Start df2redis
./bin/df2redis replicate --config examples/replicate.sample.yaml &

# 3. Run test immediately (script waits 3s before writing)
bash scripts/manual_test_all_types.sh
```

### Expected Output

```
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
🧪 RDB Phase Sync Test - All Data Types
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

[1/5] 🧹 Cleaning up existing test data...
  ✓ Cleanup completed

[2/5] ⏱  Waiting for df2redis to initialize (3 seconds)...
  ✓ Ready to write test data

[3/5] ✍️  Writing test data to source (all types)...
  ✓ Written 20 keys (4 per type)

[4/5] ⏳ Waiting for sync completion (120s)...
  ✓ Wait completed

[5/5] 🔍 Verifying synchronization...

  • Checking String keys...
    ✓ Found: df2redis_test:string:1
    ✓ Found: df2redis_test:string:2
    ✓ Found: df2redis_test:string:3
    ✓ Found: df2redis_test:string:4
  • Checking Hash keys...
    ✓ Found: df2redis_test:hash:1
    ...

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
📊 Test Results
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Total keys checked: 20
Successfully synced: 20
Missing keys: 0
Success rate: 100.00%

🎉 SUCCESS: All keys synchronized!
```

### Configuration Variables

```bash
# Modify these values to match your environment
SOURCE_HOST="localhost"         # Dragonfly host
SOURCE_PORT="16379"              # Dragonfly port
TARGET_HOST="localhost"         # Redis host
TARGET_PORT="6379"              # Redis port
TARGET_PASS=""                  # Redis password

NUM_KEYS_PER_TYPE=4             # Keys per data type
SYNC_WAIT_TIME=120              # Wait time in seconds
```

## Contributing

When adding new test scripts:
1. Add documentation to this README
2. Make scripts executable: `chmod +x scripts/your_script.py`
3. Add clear usage instructions and expected output
4. Include troubleshooting section
5. Update `requirements.txt` if adding new Python dependencies
