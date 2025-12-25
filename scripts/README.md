# df2redis Test Scripts

## Overview

This directory contains test scripts for validating df2redis functionality.

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

```bash
# 1. Install Python dependencies
pip3 install redis pyyaml

# 2. Make sure source Dragonfly has significant data (1M+ keys)
#    This ensures RDB phase takes long enough to write test data

# 3. Have a valid config.yaml in the project root
```

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

Source: 10.46.128.12:7380
Target: 10.180.7.93:6379 (redis-cluster)

✓ Connected to Source (Dragonfly): 10.46.128.12:7380
✓ Connected to Target (Redis): 10.180.7.93:6379

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

## Contributing

When adding new test scripts:
1. Add documentation to this README
2. Make scripts executable: `chmod +x scripts/your_script.py`
3. Add clear usage instructions and expected output
4. Include troubleshooting section
