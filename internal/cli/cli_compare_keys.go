package cli

import (
	"flag"
	"log"
	"os"

	"df2redis/internal/comparator"
)

func runCompareKeys(args []string) int {
	fs := flag.NewFlagSet("compare-keys", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)

	var (
		srcAddr   string
		tgtAddr   string
		password  string
		batchSize int64
	)

	fs.StringVar(&srcAddr, "src", "127.0.0.1:6379", "Source Redis/Dragonfly address")
	fs.StringVar(&tgtAddr, "tgt", "127.0.0.1:16379", "Target Redis address")
	fs.StringVar(&password, "pwd", "", "Redis password")
	fs.Int64Var(&batchSize, "batch", 1000, "Scan batch size")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		log.Printf("Failed to parse arguments: %v", err)
		return 1
	}

	cfg := comparator.Config{
		SrcAddr:   srcAddr,
		TgtAddr:   tgtAddr,
		Password:  password,
		BatchSize: batchSize,
	}

	if err := comparator.RunSimpleComparison(cfg); err != nil {
		log.Printf("Error running key comparison: %v", err)
		return 1
	}

	return 0
}
