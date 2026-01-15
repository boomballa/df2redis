package comparator

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

// Config holds the configuration for the comparator
type Config struct {
	SrcAddr   string
	TgtAddr   string
	Password  string
	BatchSize int64
	LogFile   string
}

// RunSimpleComparison executes the simple key comparison logic
func RunSimpleComparison(cfg Config) error {
	// Setup logging if file provided, otherwise use stdout (handled by caller mostly, but we can adhere to it)
	if cfg.LogFile != "" {
		f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			return fmt.Errorf("failed to open log file: %w", err)
		}
		defer f.Close()
		log.SetOutput(f)
	}

	ctx := context.Background()

	src := redis.NewClient(&redis.Options{Addr: cfg.SrcAddr, Password: cfg.Password})
	tgt := redis.NewClient(&redis.Options{Addr: cfg.TgtAddr, Password: cfg.Password})
	defer src.Close()
	defer tgt.Close()

	log.Printf("Connecting to Source: %s...", cfg.SrcAddr)
	if _, err := src.Ping(ctx).Result(); err != nil {
		return fmt.Errorf("failed to connect to source: %w", err)
	}

	log.Printf("Connecting to Target: %s...", cfg.TgtAddr)
	if _, err := tgt.Ping(ctx).Result(); err != nil {
		return fmt.Errorf("failed to connect to target: %w", err)
	}

	// 1. Load all Target keys into a Set for O(1) lookup
	// Note: For very large datasets, this might OOM. The script had this logic, so we keep it simple as requested.
	// Improvements could be bloom filters or iterative checks, but "simple" implies matching the script.
	tgtKeys := make(map[string]struct{}, 100000)
	var tgtCount int64
	log.Println("Scanning Target keys...")

	iter := tgt.Scan(ctx, 0, "", cfg.BatchSize).Iterator()
	start := time.Now()
	for iter.Next(ctx) {
		tgtKeys[iter.Val()] = struct{}{}
		tgtCount++
		if tgtCount%100000 == 0 {
			fmt.Printf("\rTarget Keys: %d", tgtCount)
		}
	}
	if err := iter.Err(); err != nil {
		return fmt.Errorf("error scanning target: %w", err)
	}
	fmt.Printf("\rTarget Scan Complete: %d keys in %v\n", tgtCount, time.Since(start))

	// 2. Scan Source and check against Target set
	log.Println("Scanning Source and comparing...")
	var srcCount, missingCount int64

	// Open file for missing keys
	missingLogName := "missing_keys.log"
	f, err := os.Create(missingLogName)
	if err != nil {
		return fmt.Errorf("failed to create missing keys log file: %w", err)
	}
	defer f.Close()

	iterSrc := src.Scan(ctx, 0, "", cfg.BatchSize).Iterator()
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
		return fmt.Errorf("error scanning source: %w", err)
	}
	fmt.Printf("\rComparison Complete: Source=%d, Target=%d, Missing=%d in %v\n",
		srcCount, tgtCount, missingCount, time.Since(startSrc))

	if missingCount > 0 {
		log.Printf("❌ Found %d missing keys! See %s for details.", missingCount, missingLogName)
		log.Printf("Missing Key Types Breakdown: %v", missingTypes)
	} else {
		log.Println("✅ Data Consistency Verified: No missing keys.")
	}

	return nil
}
