package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"time"

	"df2redis/internal/executor/fullcheck"
)

// handleCheckStart handles POST /api/check/start
func (s *DashboardServer) handleCheckStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse request body
	var req struct {
		CompareMode  int    `json:"compareMode"`  // 1=全值, 2=长度, 3=Key轮廓, 4=智能
		CompareTimes int    `json:"compareTimes"` // 比较轮次
		QPS          int    `json:"qps"`          // QPS 限制
		BatchCount   int    `json:"batchCount"`   // 批量大小
		Parallel     int    `json:"parallel"`     // 并发度
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("Invalid request: %v", err),
		})
		return
	}

	// Set default values
	if req.CompareMode <= 0 {
		req.CompareMode = 2 // Default: length comparison
	}
	if req.CompareTimes <= 0 {
		req.CompareTimes = 3 // Default: 3 rounds
	}
	if req.QPS <= 0 {
		req.QPS = 5000 // Default: 5000 (reduced to lower pressure on source and target)
	}
	if req.BatchCount <= 0 {
		req.BatchCount = 256 // Default: 256
	}
	if req.Parallel <= 0 {
		req.Parallel = 2 // Default: 2 (reduced parallelism to save resources)
	}

	// Check if a task is already running
	s.checkMu.Lock()
	if s.checkRunning {
		s.checkMu.Unlock()
		writeJSON(w, map[string]interface{}{
			"success": false,
			"message": "A validation task is already running",
		})
		return
	}

	// Mark as running
	s.checkRunning = true
	s.checkStatus = &CheckStatus{
		Running:      true,
		CompareMode:  req.CompareMode,
		CompareTimes: req.CompareTimes,
		QPS:          req.QPS,
		StartedAt:    time.Now(),
		Message:      "Initializing validation task...",
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.checkCancel = cancel
	s.checkMu.Unlock()

	// Start validation task
	go s.runRealCheckTask(ctx, req.CompareMode, req.CompareTimes, req.QPS, req.BatchCount, req.Parallel)

	writeJSON(w, map[string]interface{}{
		"success": true,
		"message": "Validation task started",
	})
}

// handleCheckStop handles POST /api/check/stop
func (s *DashboardServer) handleCheckStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.checkMu.Lock()
	defer s.checkMu.Unlock()

	if !s.checkRunning {
		writeJSON(w, map[string]interface{}{
			"success": false,
			"message": "No validation task is running",
		})
		return
	}

	// Cancel task
	if s.checkCancel != nil {
		s.checkCancel()
	}

	s.checkRunning = false
	if s.checkStatus != nil {
		s.checkStatus.Running = false
		s.checkStatus.Message = "Stopped by user"
	}

	writeJSON(w, map[string]interface{}{
		"success": true,
		"message": "Validation task stopped",
	})
}

// handleCheckStatus handles GET /api/check/status
func (s *DashboardServer) handleCheckStatus(w http.ResponseWriter, r *http.Request) {
	s.checkMu.RLock()
	defer s.checkMu.RUnlock()

	if s.checkStatus == nil {
		writeJSON(w, map[string]interface{}{
			"running": false,
			"message": "No validation task has been started",
		})
		return
	}

	// Calculate running time
	status := *s.checkStatus
	if status.Running {
		status.ElapsedSeconds = time.Since(status.StartedAt).Seconds()
	}

	writeJSON(w, status)
}

// runRealCheckTask runs real redis-full-check task
func (s *DashboardServer) runRealCheckTask(ctx context.Context, compareMode, compareTimes, qps, batchCount, parallel int) {
	defer func() {
		s.checkMu.Lock()
		s.checkRunning = false
		if s.checkStatus != nil {
			s.checkStatus.Running = false
		}
		s.checkMu.Unlock()
	}()

	// Build result file paths
	stateDir := s.cfg.StateDir
	if stateDir == "" {
		stateDir = "state"
	}
	resultDB := filepath.Join(stateDir, "check_result.db")
	resultFile := filepath.Join(stateDir, "check_result.txt")

	// Create progress channel
	progressCh := make(chan fullcheck.Progress, 100)

	// Create checker
	checker := fullcheck.NewChecker(fullcheck.CheckConfig{
		Binary:       "./bin/redis-full-check", // Relative to working directory
		SourceAddr:   s.cfg.Source.Addr,
		SourcePass:   s.cfg.Source.Password,
		TargetAddr:   s.cfg.Target.Seed,
		TargetPass:   s.cfg.Target.Password,
		CompareMode:  compareMode,
		CompareTimes: compareTimes,
		QPS:          qps,
		BatchCount:   batchCount,
		Parallel:     parallel,
		ResultDB:     resultDB,
		ResultFile:   resultFile,
	}, progressCh)

	// Start progress update goroutine
	go s.updateCheckProgressFromChannel(progressCh)

	// Execute validation
	if err := checker.Run(ctx); err != nil {
		log.Printf("[Check] Validation failed: %v", err)
		s.updateCheckStatus(func(status *CheckStatus) {
			status.Message = fmt.Sprintf("Validation failed: %v", err)
			status.Running = false
		})
		return
	}

	// Validation completed
	s.updateCheckStatus(func(status *CheckStatus) {
		status.Progress = 1.0
		status.Message = "Validation completed"
		status.Running = false
		// Ensure final round is displayed
		status.Round = status.CompareTimes
	})

	log.Println("[Check] Validation task completed")
}

// updateCheckProgressFromChannel updates status from progress channel
func (s *DashboardServer) updateCheckProgressFromChannel(progressCh <-chan fullcheck.Progress) {
	for progress := range progressCh {
		s.updateCheckStatus(func(status *CheckStatus) {
			status.Round = progress.Round
			status.TotalKeys = progress.TotalKeys
			status.CheckedKeys = progress.CheckedKeys
			status.ConsistentKeys = progress.ConsistentKeys
			status.InconsistentKeys = progress.ConflictKeys + progress.MissingKeys
			status.ErrorCount = progress.ErrorCount
			status.Message = progress.Message

			// Calculate overall progress across rounds
			// Overall progress = (completed rounds + current round progress) / total rounds
			if progress.CompareTimes > 0 && progress.Round > 0 {
				completedRounds := float64(progress.Round - 1)
				currentRoundProgress := progress.Progress
				status.Progress = (completedRounds + currentRoundProgress) / float64(progress.CompareTimes)

				// Ensure progress does not exceed 1.0
				if status.Progress > 1.0 {
					status.Progress = 1.0
				}
			} else {
				status.Progress = progress.Progress
			}
		})
	}
}

// updateCheckStatus thread-safe check status update
func (s *DashboardServer) updateCheckStatus(fn func(*CheckStatus)) {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	if s.checkStatus != nil {
		fn(s.checkStatus)
	}
}
