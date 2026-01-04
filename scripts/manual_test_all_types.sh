#!/bin/bash

# Manual test script for RDB phase synchronization
# Tests all 5 Redis data types: String, Hash, List, Set, ZSet

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration (modify these values as needed)
# For actual test environment configs, see CLAUDE.md "Internal Development Reference"
SOURCE_HOST="localhost"
SOURCE_PORT="6380"
TARGET_HOST="localhost"
TARGET_PORT="6379"
TARGET_PASS=""

TEST_PREFIX="df2redis_test:"
NUM_KEYS_PER_TYPE=4
SYNC_WAIT_TIME=120

echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}🧪 RDB Phase Sync Test - All Data Types${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

# Step 1: Clean up existing test data
echo -e "${YELLOW}[1/5] 🧹 Cleaning up existing test data...${NC}"

# Clean source
redis-cli -h $SOURCE_HOST -p $SOURCE_PORT <<EOF >/dev/null 2>&1 || true
$(for i in $(seq 1 $NUM_KEYS_PER_TYPE); do
    echo "DEL ${TEST_PREFIX}string:$i"
    echo "DEL ${TEST_PREFIX}hash:$i"
    echo "DEL ${TEST_PREFIX}list:$i"
    echo "DEL ${TEST_PREFIX}set:$i"
    echo "DEL ${TEST_PREFIX}zset:$i"
done)
EOF

# Clean target
redis-cli -h $TARGET_HOST -p $TARGET_PORT -a $TARGET_PASS <<EOF >/dev/null 2>&1 || true
$(for i in $(seq 1 $NUM_KEYS_PER_TYPE); do
    echo "DEL ${TEST_PREFIX}string:$i"
    echo "DEL ${TEST_PREFIX}hash:$i"
    echo "DEL ${TEST_PREFIX}list:$i"
    echo "DEL ${TEST_PREFIX}set:$i"
    echo "DEL ${TEST_PREFIX}zset:$i"
done)
EOF

echo -e "${GREEN}  ✓ Cleanup completed${NC}"
echo ""

# Step 2: Wait for df2redis to start (user should have it running)
echo -e "${YELLOW}[2/5] ⏱  Waiting for df2redis to initialize (3 seconds)...${NC}"
sleep 3
echo -e "${GREEN}  ✓ Ready to write test data${NC}"
echo ""

# Step 3: Write test data to source
echo -e "${YELLOW}[3/5] ✍️  Writing test data to source (all types)...${NC}"

redis-cli -h $SOURCE_HOST -p $SOURCE_PORT <<EOF >/dev/null
$(for i in $(seq 1 $NUM_KEYS_PER_TYPE); do
    # String
    echo "SET ${TEST_PREFIX}string:$i \"test_value_$i\""

    # Hash
    echo "HSET ${TEST_PREFIX}hash:$i field1 value1_$i field2 value2_$i field3 value3_$i"

    # List
    echo "RPUSH ${TEST_PREFIX}list:$i elem1_$i elem2_$i elem3_$i elem4_$i"

    # Set
    echo "SADD ${TEST_PREFIX}set:$i member1_$i member2_$i member3_$i member4_$i"

    # ZSet
    echo "ZADD ${TEST_PREFIX}zset:$i 1.0 member1_$i 2.0 member2_$i 3.0 member3_$i 4.0 member4_$i"
done)
EOF

TOTAL_KEYS=$((NUM_KEYS_PER_TYPE * 5))
echo -e "${GREEN}  ✓ Written $TOTAL_KEYS keys (${NUM_KEYS_PER_TYPE} per type)${NC}"
echo ""

# Step 4: Wait for synchronization
echo -e "${YELLOW}[4/5] ⏳ Waiting for sync completion (${SYNC_WAIT_TIME}s)...${NC}"
sleep $SYNC_WAIT_TIME
echo -e "${GREEN}  ✓ Wait completed${NC}"
echo ""

# Step 5: Verify synchronization
echo -e "${YELLOW}[5/5] 🔍 Verifying synchronization...${NC}"
echo ""

MISSING_KEYS=0
TOTAL_CHECKS=0

# Check String keys
echo -e "${BLUE}  • Checking String keys...${NC}"
for i in $(seq 1 $NUM_KEYS_PER_TYPE); do
    KEY="${TEST_PREFIX}string:$i"
    TOTAL_CHECKS=$((TOTAL_CHECKS + 1))
    RESULT=$(redis-cli -h $TARGET_HOST -p $TARGET_PORT -a $TARGET_PASS GET "$KEY" 2>/dev/null || echo "")
    if [ -z "$RESULT" ] || [ "$RESULT" = "(nil)" ]; then
        echo -e "${RED}    ✗ Missing: $KEY${NC}"
        MISSING_KEYS=$((MISSING_KEYS + 1))
    else
        echo -e "${GREEN}    ✓ Found: $KEY${NC}"
    fi
done

# Check Hash keys
echo -e "${BLUE}  • Checking Hash keys...${NC}"
for i in $(seq 1 $NUM_KEYS_PER_TYPE); do
    KEY="${TEST_PREFIX}hash:$i"
    TOTAL_CHECKS=$((TOTAL_CHECKS + 1))
    RESULT=$(redis-cli -h $TARGET_HOST -p $TARGET_PORT -a $TARGET_PASS HGETALL "$KEY" 2>/dev/null || echo "")
    if [ -z "$RESULT" ] || [ "$RESULT" = "(empty array)" ]; then
        echo -e "${RED}    ✗ Missing: $KEY${NC}"
        MISSING_KEYS=$((MISSING_KEYS + 1))
    else
        echo -e "${GREEN}    ✓ Found: $KEY${NC}"
    fi
done

# Check List keys
echo -e "${BLUE}  • Checking List keys...${NC}"
for i in $(seq 1 $NUM_KEYS_PER_TYPE); do
    KEY="${TEST_PREFIX}list:$i"
    TOTAL_CHECKS=$((TOTAL_CHECKS + 1))
    RESULT=$(redis-cli -h $TARGET_HOST -p $TARGET_PORT -a $TARGET_PASS LRANGE "$KEY" 0 -1 2>/dev/null || echo "")
    if [ -z "$RESULT" ] || [ "$RESULT" = "(empty array)" ]; then
        echo -e "${RED}    ✗ Missing: $KEY${NC}"
        MISSING_KEYS=$((MISSING_KEYS + 1))
    else
        echo -e "${GREEN}    ✓ Found: $KEY${NC}"
    fi
done

# Check Set keys
echo -e "${BLUE}  • Checking Set keys...${NC}"
for i in $(seq 1 $NUM_KEYS_PER_TYPE); do
    KEY="${TEST_PREFIX}set:$i"
    TOTAL_CHECKS=$((TOTAL_CHECKS + 1))
    RESULT=$(redis-cli -h $TARGET_HOST -p $TARGET_PORT -a $TARGET_PASS SMEMBERS "$KEY" 2>/dev/null || echo "")
    if [ -z "$RESULT" ] || [ "$RESULT" = "(empty array)" ]; then
        echo -e "${RED}    ✗ Missing: $KEY${NC}"
        MISSING_KEYS=$((MISSING_KEYS + 1))
    else
        echo -e "${GREEN}    ✓ Found: $KEY${NC}"
    fi
done

# Check ZSet keys
echo -e "${BLUE}  • Checking ZSet keys...${NC}"
for i in $(seq 1 $NUM_KEYS_PER_TYPE); do
    KEY="${TEST_PREFIX}zset:$i"
    TOTAL_CHECKS=$((TOTAL_CHECKS + 1))
    RESULT=$(redis-cli -h $TARGET_HOST -p $TARGET_PORT -a $TARGET_PASS ZRANGE "$KEY" 0 -1 2>/dev/null || echo "")
    if [ -z "$RESULT" ] || [ "$RESULT" = "(empty array)" ]; then
        echo -e "${RED}    ✗ Missing: $KEY${NC}"
        MISSING_KEYS=$((MISSING_KEYS + 1))
    else
        echo -e "${GREEN}    ✓ Found: $KEY${NC}"
    fi
done

echo ""
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}📊 Test Results${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"

SYNCED_KEYS=$((TOTAL_CHECKS - MISSING_KEYS))
SUCCESS_RATE=$(awk "BEGIN {printf \"%.2f\", ($SYNCED_KEYS / $TOTAL_CHECKS) * 100}")

echo -e "Total keys checked: ${BLUE}$TOTAL_CHECKS${NC}"
echo -e "Successfully synced: ${GREEN}$SYNCED_KEYS${NC}"
echo -e "Missing keys: ${RED}$MISSING_KEYS${NC}"
echo -e "Success rate: ${YELLOW}$SUCCESS_RATE%${NC}"
echo ""

if [ $MISSING_KEYS -eq 0 ]; then
    echo -e "${GREEN}🎉 SUCCESS: All keys synchronized!${NC}"
    exit 0
else
    echo -e "${RED}❌ FAILURE: $MISSING_KEYS keys not synchronized${NC}"
    exit 1
fi
