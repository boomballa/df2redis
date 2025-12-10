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

	// 设置默认值
	if req.CompareMode <= 0 {
		req.CompareMode = 2 // 默认：长度比较
	}
	if req.CompareTimes <= 0 {
		req.CompareTimes = 3 // 默认：3 轮
	}
	if req.QPS <= 0 {
		req.QPS = 5000 // 默认：5000（降低以减少对源库和目标库的压力）
	}
	if req.BatchCount <= 0 {
		req.BatchCount = 256 // 默认：256
	}
	if req.Parallel <= 0 {
		req.Parallel = 2 // 默认：2（降低并发以减少资源消耗）
	}

	// 检查是否已有任务运行
	s.checkMu.Lock()
	if s.checkRunning {
		s.checkMu.Unlock()
		writeJSON(w, map[string]interface{}{
			"success": false,
			"message": "A validation task is already running",
		})
		return
	}

	// 标记为运行中
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

	// 启动校验任务
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

	// 取消任务
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

	// 计算运行时间
	status := *s.checkStatus
	if status.Running {
		status.ElapsedSeconds = time.Since(status.StartedAt).Seconds()
	}

	writeJSON(w, status)
}

// runRealCheckTask 运行真实的 redis-full-check 任务
func (s *DashboardServer) runRealCheckTask(ctx context.Context, compareMode, compareTimes, qps, batchCount, parallel int) {
	defer func() {
		s.checkMu.Lock()
		s.checkRunning = false
		if s.checkStatus != nil {
			s.checkStatus.Running = false
		}
		s.checkMu.Unlock()
	}()

	// 构建结果文件路径
	stateDir := s.cfg.StateDir
	if stateDir == "" {
		stateDir = "state"
	}
	resultDB := filepath.Join(stateDir, "check_result.db")
	resultFile := filepath.Join(stateDir, "check_result.txt")

	// 创建进度通道
	progressCh := make(chan fullcheck.Progress, 100)

	// 创建校验器
	checker := fullcheck.NewChecker(fullcheck.CheckConfig{
		Binary:       "./bin/redis-full-check", // 相对于工作目录
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

	// 启动进度更新协程
	go s.updateCheckProgressFromChannel(progressCh)

	// 执行校验
	if err := checker.Run(ctx); err != nil {
		log.Printf("[Check] 校验失败: %v", err)
		s.updateCheckStatus(func(status *CheckStatus) {
			status.Message = fmt.Sprintf("Validation failed: %v", err)
			status.Running = false
		})
		return
	}

	// 校验完成
	s.updateCheckStatus(func(status *CheckStatus) {
		status.Progress = 1.0
		status.Message = "Validation completed"
		status.Running = false
		// 确保显示最终轮次
		status.Round = status.CompareTimes
	})

	log.Println("[Check] 校验任务完成")
}

// updateCheckProgressFromChannel 从进度通道更新状态
func (s *DashboardServer) updateCheckProgressFromChannel(progressCh <-chan fullcheck.Progress) {
	for progress := range progressCh {
		s.updateCheckStatus(func(status *CheckStatus) {
			status.Round = progress.Round
			status.TotalKeys = progress.TotalKeys
			status.CheckedKeys = progress.CheckedKeys
			status.ConsistentKeys = progress.ConsistentKeys
			status.InconsistentKeys = progress.ConflictKeys + progress.MissingKeys
			status.ErrorCount = progress.ErrorCount
			status.Progress = progress.Progress
			status.Message = progress.Message
		})
	}
}

// updateCheckStatus 线程安全地更新校验状态
func (s *DashboardServer) updateCheckStatus(fn func(*CheckStatus)) {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	if s.checkStatus != nil {
		fn(s.checkStatus)
	}
}
