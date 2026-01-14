package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	srcAddr  = flag.String("src", "127.0.0.1:6379", "Source Redis/Dragonfly address")
	tgtAddr  = flag.String("tgt", "127.0.0.1:16379", "Target Redis address")
	password = flag.String("pwd", "", "Redis password")
	batch    = flag.Int64("batch", 1000, "Scan batch size")
)

func main() {
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lmicroseconds)
	ctx := context.Background()

	src := redis.NewClient(&redis.Options{Addr: *srcAddr, Password: *password})
	tgt := redis.NewClient(&redis.Options{Addr: *tgtAddr, Password: *password})

	log.Printf("Connecting to Source: %s...", *srcAddr)
	if _, err := src.Ping(ctx).Result(); err != nil {
		log.Fatalf("Failed to connect to source: %v", err)
	}

	log.Printf("Connecting to Target: %s...", *tgtAddr)
	if _, err := tgt.Ping(ctx).Result(); err != nil {
		log.Fatalf("Failed to connect to target: %v", err)
	}

	// 1. Load all Target keys into a Set for O(1) lookup
	tgtKeys := make(map[string]struct{}, 100000)
	var tgtCount int64
	log.Println("Scanning Target keys...")

	iter := tgt.Scan(ctx, 0, "", *batch).Iterator()
	start := time.Now()
	for iter.Next(ctx) {
		tgtKeys[iter.Val()] = struct{}{}
		tgtCount++
		if tgtCount%100000 == 0 {
			fmt.Printf("\rTarget Keys: %d", tgtCount)
		}
	}
	if err := iter.Err(); err != nil {
		log.Fatalf("Error scanning target: %v", err)
	}
	fmt.Printf("\rTarget Scan Complete: %d keys in %v\n", tgtCount, time.Since(start))

	// 2. Scan Source and check against Target set
	log.Println("Scanning Source and comparing...")
	var srcCount, missingCount int64

	// Open file for missing keys
	f, err := os.Create("missing_keys.log")
	if err != nil {
		log.Fatalf("Failed to create log file: %v", err)
	}
	defer f.Close()

	iterSrc := src.Scan(ctx, 0, "", *batch).Iterator()
	startSrc := time.Now()

	// Track types of missing keys
	missingTypes := make(map[string]int)

	for iterSrc.Next(ctx) {
		key := iterSrc.Val()
		srcCount++

		if _, exists := tgtKeys[key]; !exists {
			missingCount++

			// Get type of missing key
			keyType, _ := src.Type(ctx, key).Result()
			missingTypes[keyType]++

			// Log missing key with type
			f.WriteString(fmt.Sprintf("%s (%s)\n", key, keyType))
		}

		if srcCount%100000 == 0 {
			fmt.Printf("\rSource Processed: %d | Missing: %d", srcCount, missingCount)
		}
	}
	if err := iterSrc.Err(); err != nil {
		log.Fatalf("Error scanning source: %v", err)
	}
	fmt.Printf("\rComparison Complete: Source=%d, Target=%d, Missing=%d in %v\n",
		srcCount, tgtCount, missingCount, time.Since(startSrc))

	if missingCount > 0 {
		log.Printf("❌ Found %d missing keys! See missing_keys.log for details.", missingCount)
		log.Printf("Missing Key Types Breakdown: %v", missingTypes)
	} else {
		log.Println("✅ Data Consistency Verified: No missing keys.")
	}
}
