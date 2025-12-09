package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// handleCheckStart handles POST /api/check/start
func (s *DashboardServer) handleCheckStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse request body
	var req struct {
		Mode       string `json:"mode"`       // "sampling" or "full"
		SampleSize int    `json:"sampleSize"` // For sampling mode
		KeyPrefix  string `json:"keyPrefix"`  // Optional key filter
		QPS        int    `json:"qps"`        // QPS limit
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("Invalid request: %v", err),
		})
		return
	}

	// Validate parameters
	if req.Mode != "sampling" && req.Mode != "full" {
		writeJSON(w, map[string]interface{}{
			"success": false,
			"message": "Invalid mode, must be 'sampling' or 'full'",
		})
		return
	}

	if req.Mode == "sampling" && req.SampleSize <= 0 {
		req.SampleSize = 1000 // Default sample size
	}

	if req.QPS <= 0 {
		req.QPS = 100 // Default QPS
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

	// Start new check task
	s.checkRunning = true
	s.checkStatus = &CheckStatus{
		Running:     true,
		Mode:        req.Mode,
		SampleSize:  req.SampleSize,
		KeyPrefix:   req.KeyPrefix,
		QPS:         req.QPS,
		StartedAt:   time.Now(),
		TotalKeys:   int64(req.SampleSize),
		Message:     "Initializing validation task...",
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.checkCancel = cancel
	s.checkMu.Unlock()

	// Start check in background
	go s.runCheckTask(ctx, req.Mode, req.SampleSize, req.KeyPrefix, req.QPS)

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

	// Cancel the running task
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

	// Calculate elapsed time
	status := *s.checkStatus
	if status.Running {
		status.ElapsedSeconds = time.Since(status.StartedAt).Seconds()

		// Calculate progress
		if status.TotalKeys > 0 {
			status.Progress = float64(status.CheckedKeys) / float64(status.TotalKeys)
			if status.Progress > 1.0 {
				status.Progress = 1.0
			}
		}

		// Estimate remaining time
		if status.CheckedKeys > 0 && status.Progress > 0 {
			status.EstimatedSeconds = status.ElapsedSeconds / status.Progress
		}
	}

	writeJSON(w, status)
}

// runCheckTask simulates a validation task (MVP版本 - 模拟实现)
func (s *DashboardServer) runCheckTask(ctx context.Context, mode string, sampleSize int, keyPrefix string, qps int) {
	defer func() {
		s.checkMu.Lock()
		s.checkRunning = false
		if s.checkStatus != nil {
			s.checkStatus.Running = false
		}
		s.checkMu.Unlock()
	}()

	totalKeys := int64(sampleSize)
	if mode == "full" {
		totalKeys = 100000 // 模拟全量总数
	}

	tickInterval := time.Second / time.Duration(qps) // QPS 控制
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	s.updateCheckStatus(func(status *CheckStatus) {
		status.TotalKeys = totalKeys
		status.Message = "Validating keys..."
	})

	for i := int64(0); i < totalKeys; i++ {
		select {
		case <-ctx.Done():
			s.updateCheckStatus(func(status *CheckStatus) {
				status.Message = "Validation stopped"
			})
			return
		case <-ticker.C:
			// 模拟检查一个 key
			s.updateCheckStatus(func(status *CheckStatus) {
				status.CheckedKeys = i + 1

				// 模拟结果：98% 一致，2% 不一致
				if i%50 == 0 {
					status.InconsistentKeys++
				} else {
					status.ConsistentKeys++
				}

				// 模拟极少错误
				if i%500 == 0 && i > 0 {
					status.ErrorCount++
				}

				status.Message = fmt.Sprintf("Checking key %d/%d...", i+1, totalKeys)
			})
		}
	}

	s.updateCheckStatus(func(status *CheckStatus) {
		status.Progress = 1.0
		status.Message = "Validation completed"
		status.Running = false
	})
}

// updateCheckStatus safely updates check status
func (s *DashboardServer) updateCheckStatus(fn func(*CheckStatus)) {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	if s.checkStatus != nil {
		fn(s.checkStatus)
	}
}
