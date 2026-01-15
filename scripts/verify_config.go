//go:build ignore

package main

import (
	"fmt"
	"os"

	"df2redis/internal/config"
)

func main() {
	cfgPath := "examples/replicate.sample.yaml"
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Config Loaded: %s\n", cfgPath)
	fmt.Printf("Log.ConsoleEnabled ptr: %v\n", cfg.Log.ConsoleEnabled)
	if cfg.Log.ConsoleEnabled != nil {
		fmt.Printf("Log.ConsoleEnabled value: %v\n", *cfg.Log.ConsoleEnabled)
	}
	fmt.Printf("Log.ConsoleEnabledValue(): %v\n", cfg.Log.ConsoleEnabledValue())
}
