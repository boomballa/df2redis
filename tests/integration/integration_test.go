package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Source struct {
		Addr     string `yaml:"addr"`
		Password string `yaml:"password"`
	} `yaml:"source"`
	Target struct {
		Addr     string `yaml:"addr"`
		Password string `yaml:"password"`
	} `yaml:"target"`
}

func TestReplication(t *testing.T) {
	// 1. Read configuration
	configPath := "integration.yaml"
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Skip("Skipping integration test: integration.yaml not found. Copy integration.sample.yaml to run.")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read config: %v", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("Failed to parse config: %v", err)
	}

	ctx := context.Background()

	// 2. Connect to Source (Dragonfly)
	rdbSource := redis.NewClient(&redis.Options{
		Addr:     cfg.Source.Addr,
		Password: cfg.Source.Password,
	})
	defer rdbSource.Close()

	if err := rdbSource.Ping(ctx).Err(); err != nil {
		t.Skipf("Skipping integration test: Source unavailable (%v)", err)
	}

	// 3. Connect to Target (Redis)
	rdbTarget := redis.NewClient(&redis.Options{
		Addr:     cfg.Target.Addr,
		Password: cfg.Target.Password,
	})
	defer rdbTarget.Close()

	if err := rdbTarget.Ping(ctx).Err(); err != nil {
		t.Skipf("Skipping integration test: Target unavailable (%v)", err)
	}

	// 4. Setup Test Data
	testKey := "test:integration:key"
	testValue := fmt.Sprintf("value-%d", time.Now().UnixNano())

	t.Logf("Writing test key %s to Source...", testKey)
	if err := rdbSource.Set(ctx, testKey, testValue, 0).Err(); err != nil {
		t.Fatalf("Failed to write to source: %v", err)
	}

	// 5. Build and Run df2redis
	// Build binary
	cmdBuild := exec.Command("go", "build", "-o", "df2redis-integration", "../../cmd/df2redis")
	if out, err := cmdBuild.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build df2redis: %s", out)
	}
	defer os.Remove("df2redis-integration")

	// Run replicate command
	// We run it in background or with a timeout because it's a long-running process
	// Ideally, we run 'migrate' mode which exits after snapshot, but let's try 'replicate' for a few seconds
	// actually, let's use 'migrate' if available, otherwise 'replicate' and kill it.
	// The README says 'migrate' is available.

	t.Log("Starting df2redis migration...")
	cmdRun := exec.Command("./df2redis-integration", "migrate", "--config", configPath)

	// Capture output for debugging
	// cmdRun.Stdout = os.Stdout
	// cmdRun.Stderr = os.Stderr

	if err := cmdRun.Start(); err != nil {
		t.Fatalf("Failed to start df2redis: %v", err)
	}

	// Wait for compilation (migrate mode should exit)
	// Or set a timeout
	done := make(chan error, 1)
	go func() {
		done <- cmdRun.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("df2redis execution failed: %v", err)
		}
	case <-time.After(30 * time.Second):
		if err := cmdRun.Process.Kill(); err != nil {
			t.Fatal("Failed to kill timed-out process:", err)
		}
		t.Fatal("Integration test timed out")
	}

	// 6. Verify Data on Target
	t.Log("Verifying data on Target...")

	// Retry a few times as replication might have slight delay (though migrate should ensure completion)
	var got string
	var getErr error
	for i := 0; i < 5; i++ {
		got, getErr = rdbTarget.Get(ctx, testKey).Result()
		if getErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if getErr != nil {
		t.Fatalf("Failed to get key from target: %v", getErr)
	}

	if got != testValue {
		t.Errorf("Value mismatch! Want: %s, Got: %s", testValue, got)
	} else {
		t.Log("SUCCESS: Data replicated correctly!")
	}
}
