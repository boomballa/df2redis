package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"df2redis/internal/checker"
)

// handleCheckStart handles POST /api/check/start
func (s *DashboardServer) handleCheckStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse request body
	var req struct {
		CompareMode  int `json:"compareMode"`  // 1=full, 2=length, 3=outline, 4=smart
		CompareTimes int `json:"compareTimes"` // number of comparison rounds
		QPS          int `json:"qps"`          // QPS limit
		BatchCount   int `json:"batchCount"`   // batch size
		Parallel     int `json:"parallel"`     // worker count
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("Invalid request: %v", err),
		})
		return
	}

	// Set default values matching legacy full-check behavior
	// 1=full, 2=length (default), 3=outline, 4=smart
	if req.CompareMode <= 0 {
		req.CompareMode = 2
	}
	if req.CompareTimes <= 0 {
		req.CompareTimes = 3
	}
	if req.QPS <= 0 {
		req.QPS = 5000
	}
	if req.BatchCount <= 0 {
		req.BatchCount = 256
	}
	if req.Parallel <= 0 {
		req.Parallel = 2
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
		Running:       true,
		CompareMode:   req.CompareMode, // Store int for UI compatibility
		CompareTimes:  req.CompareTimes,
		QPS:           req.QPS,
		StartedAt:     time.Now(),
		RoundProgress: 0,
		Message:       "Initializing validation task...",
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

// runRealCheckTask runs real native checker task
func (s *DashboardServer) runRealCheckTask(ctx context.Context, compareModeInt int, compareTimes, qps, batchCount, parallel int) {
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

	// Create progress channel
	progressCh := make(chan checker.Progress, 100)

	// Map legacy int mode to native checker.CheckMode
	var cm checker.CheckMode
	switch compareModeInt {
	case 1:
		cm = checker.ModeFullValue
	case 2:
		cm = checker.ModeValueLength
	case 4:
		cm = checker.ModeSmartBigKey
	case 3:
		fallthrough
	default:
		cm = checker.ModeKeyOutline
	}

	// Create checker options
	cfg := checker.Config{
		SourceAddr:     s.cfg.Source.Addr,
		SourcePassword: s.cfg.Source.Password,
		TargetAddr:     s.cfg.Target.Addr,
		TargetPassword: s.cfg.Target.Password,
		Mode:           cm,
		QPS:            qps,
		Parallel:       parallel,
		ResultDir:      stateDir,
		BatchSize:      batchCount,
		Timeout:        3600,
		CompareTimes:   compareTimes,
		TaskName:       "dashboard-check",
	}

	c := checker.NewChecker(cfg)

	// Start progress update goroutine
	go s.updateCheckProgressFromChannel(progressCh)

	// Execute validation
	result, err := c.Run(ctx, progressCh)

	// Close progress channel after run returns
	close(progressCh)

	if err != nil {
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
		status.RoundProgress = 1.0
		status.Message = "Validation completed"
		status.Running = false
		status.ElapsedSeconds = time.Since(status.StartedAt).Seconds()

		status.TotalKeys = result.TotalKeys
		status.ConsistentKeys = result.ConsistentKeys
		status.InconsistentKeys = result.InconsistentKeys
	})

	log.Println("[Check] Validation task completed")
}

// updateCheckProgressFromChannel updates status from progress channel
func (s *DashboardServer) updateCheckProgressFromChannel(progressCh <-chan checker.Progress) {
	for progress := range progressCh {
		s.updateCheckStatus(func(status *CheckStatus) {
			status.TotalKeys = progress.TotalKeys
			status.CheckedKeys = progress.CheckedKeys
			status.ConsistentKeys = progress.ConsistentKeys
			status.InconsistentKeys = progress.InconsistentKeys
			status.Message = progress.Message
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
