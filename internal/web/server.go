package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"df2redis/internal/config"
	"df2redis/internal/logger"
	"df2redis/internal/state"
)

// DashboardServer exposes a simple web UI for df2redis state.
type DashboardServer struct {
	addr       string
	cfg        *config.Config
	store      *state.Store
	tmpl       *template.Template
	snapshotMu sync.RWMutex
	snapshot   state.Snapshot

	// Callback for dynamic configuration
	onConfigUpdate func(qps int, batchSize int) error

	// Check task management
	checkMu      sync.RWMutex
	checkRunning bool
	checkCancel  context.CancelFunc
	checkStatus  *CheckStatus

	// Dedicated logger for dashboard events
	logger *log.Logger
}

// Options configure the dashboard server.
type Options struct {
	Addr           string
	Cfg            *config.Config
	Store          *state.Store
	OnConfigUpdate func(qps int, batchSize int) error
}

// New creates a dashboard server.
func New(opts Options) (*DashboardServer, error) {
	tmpl, err := loadTemplates()
	if err != nil {
		return nil, err
	}

	// Setup separate dashboard logger
	logDir := opts.Cfg.Log.Dir
	if logDir == "" {
		logDir = "log" // Default fallback
	}
	// resolve absolute path for log dir
	absLogDir := opts.Cfg.ResolvePath(logDir)

	// Determine dashboard log filename
	logName := "dashboard.log"
	if opts.Cfg.TaskName != "" {
		logName = fmt.Sprintf("%s_dashboard.log", opts.Cfg.TaskName)
	}

	// Create independent logger
	dashLogger, err := logger.NewStandaloneLogger(
		filepath.Join(absLogDir, logName),
		"[dashboard] ",
	)
	if err != nil {
		// Fallback to stderr if file creation fails
		dashLogger = log.New(os.Stderr, "[dashboard] ", log.LstdFlags)
		fmt.Printf("Failed to create dashboard log file: %v\n", err)
	}

	return &DashboardServer{
		addr:           opts.Addr,
		cfg:            opts.Cfg,
		store:          opts.Store,
		tmpl:           tmpl,
		onConfigUpdate: opts.OnConfigUpdate,
		logger:         dashLogger,
	}, nil
}

// allocateSmartPort attempts to find an available port using intelligent strategies:
// 1. If preferredAddr specifies a port (e.g. ":7777"), try it first
// 2. If port 0 or unavailable, randomly select from range 20000-30000
// 3. Test each candidate port for availability
// 4. Retry up to maxRetries times
// Returns the successfully bound listener and actual address.
func allocateSmartPort(preferredAddr string, maxRetries int) (net.Listener, string, error) {
	const (
		portRangeMin = 20000
		portRangeMax = 30000
	)

	// Helper: test if a port is available
	tryPort := func(addr string) (net.Listener, string, error) {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, "", err
		}
		actualAddr := ln.Addr().String()
		return ln, actualAddr, nil
	}

	// Strategy 1: Try preferred port if specified
	if preferredAddr != "" && preferredAddr != ":0" {
		if ln, addr, err := tryPort(preferredAddr); err == nil {
			return ln, addr, nil
		}
		// Preferred port failed, log and fall through to random allocation
		log.Printf("⚠️  Preferred port %s unavailable, trying random allocation...", preferredAddr)
	}

	// Strategy 2: Random port allocation in safe range (20000-30000)
	for i := 0; i < maxRetries; i++ {
		randomPort := portRangeMin + rand.Intn(portRangeMax-portRangeMin+1)
		addr := fmt.Sprintf(":%d", randomPort)

		if ln, actualAddr, err := tryPort(addr); err == nil {
			log.Printf("✓ Smart port allocation: selected %s (attempt %d/%d)", actualAddr, i+1, maxRetries)
			return ln, actualAddr, nil
		}
		// Port conflict, try next random port
	}

	return nil, "", fmt.Errorf("failed to allocate port after %d attempts", maxRetries)
}

// Start runs HTTP server. Blocks until server stops.
// When ready is not nil it will receive the actual listen address once the port is bound.
func (s *DashboardServer) Start(ready chan<- string) error {
	if s.addr == "" {
		s.addr = ":8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/sync/summary", s.handleSyncSummary)
	mux.HandleFunc("/api/sync/progress", s.handleSyncProgress)
	mux.HandleFunc("/api/check/latest", s.handleCheckLatest)
	mux.HandleFunc("/api/events", s.handleEvents)

	mux.HandleFunc("/api/logs", s.handleLogs)
	mux.HandleFunc("/api/config", s.handleConfigUpdate) // New Config API
	// Check validation API endpoints
	mux.HandleFunc("/api/check/start", s.handleCheckStart)
	mux.HandleFunc("/api/check/stop", s.handleCheckStop)
	mux.HandleFunc("/api/check/status", s.handleCheckStatus)
	fileServer := http.FileServer(http.Dir(staticDir()))
	mux.HandleFunc("/static/", func(w http.ResponseWriter, r *http.Request) {
		// Disable caching for static files to ensure updates are loaded immediately
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/static")
		fileServer.ServeHTTP(w, r)
	})

	go s.refreshLoop()

	// Use smart port allocation with retry mechanism
	ln, actualAddr, err := allocateSmartPort(s.addr, 10)
	if err != nil {
		return fmt.Errorf("failed to allocate dashboard port: %w", err)
	}

	s.addr = actualAddr
	if ready != nil {
		ready <- actualAddr
	}
	s.logger.Printf("🌐 Dashboard listening at http://%s", actualAddr)
	log.Printf("🌐 Dashboard listening at http://%s", actualAddr)

	server := &http.Server{Handler: mux, ErrorLog: s.logger}
	return server.Serve(ln)
}

func (s *DashboardServer) refreshLoop() {
	for {
		snap, err := s.store.Load()
		if err == nil {
			s.snapshotMu.Lock()
			s.snapshot = snap
			s.snapshotMu.Unlock()
		}
		time.Sleep(3 * time.Second)
	}
}

func (s *DashboardServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	// Use relative paths for cleaner display
	stateDir := s.cfg.StateDir
	statusFile := s.cfg.StatusFile

	// Get actual log file path from logger (e.g., log/dba-song-test_replicate.log)
	logFile := logger.GetLogFilePath()
	if logFile == "" {
		// Fallback: infer log file path from config
		logFile = s.inferLogFilePath()
	}

	ctx := map[string]interface{}{
		"StateDir":     stateDir,
		"StatusFile":   statusFile,
		"LogFile":      logFile,
		"GeneratedAt":  time.Now().Format(time.RFC3339),
		"Source":       s.cfg.Source,
		"Target":       s.cfg.Target,
		"SnapshotPath": s.cfg.Migrate.SnapshotPath,
		"TaskName":     s.cfg.TaskName,
	}
	s.snapshotMu.RLock()
	ctx["Snapshot"] = s.snapshot
	s.snapshotMu.RUnlock()

	if err := s.tmpl.ExecuteTemplate(w, "layout.html", ctx); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *DashboardServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	snap := s.currentSnapshot()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

func (s *DashboardServer) handleSyncSummary(w http.ResponseWriter, r *http.Request) {
	resp := buildSyncSummary(s.cfg, s.currentSnapshot())
	writeJSON(w, resp)
}

func (s *DashboardServer) handleSyncProgress(w http.ResponseWriter, r *http.Request) {
	resp := buildSyncProgress(s.cfg, s.currentSnapshot())
	writeJSON(w, resp)
}

func (s *DashboardServer) handleCheckLatest(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, buildCheckResponse(s.currentSnapshot()))
}

func (s *DashboardServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	snap := s.currentSnapshot()
	writeJSON(w, map[string]interface{}{
		"events": snap.Events,
	})
}

func (s *DashboardServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	// Parse query parameters
	linesParam := r.URL.Query().Get("lines")
	offsetParam := r.URL.Query().Get("offset")

	lines := 100 // default: 100 lines
	if linesParam != "" {
		if parsed, err := strconv.Atoi(linesParam); err == nil && parsed > 0 {
			lines = parsed
		}
	}

	// Determine mode
	mode := r.URL.Query().Get("mode")

	offset := 0 // default: start from beginning
	if mode == "tail" {
		offset = -1 // Signal to readLogFile to fetch the last N lines
	} else if offsetParam != "" {
		if parsed, err := strconv.Atoi(offsetParam); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	// Determine actual log file path
	logPath := logger.GetLogFilePath()
	if logPath == "" {
		// Fallback: infer log file path from config
		logPath = s.inferLogFilePath()
	}

	s.logger.Printf("Reading logs from: %s (offset=%d, lines=%d)", logPath, offset, lines)

	// Read log file
	content, err := readLogFile(logPath, offset, lines)
	if err != nil {
		s.logger.Printf("Failed to read log file %s: %v", logPath, err)
		writeJSON(w, map[string]interface{}{
			"error":  fmt.Sprintf("failed to read log file: %v", err),
			"lines":  []string{},
			"total":  0,
			"offset": offset,
		})
		return
	}

	s.logger.Printf("Successfully read %d lines from log file (total: %d)", len(content.Lines), content.TotalLines)

	writeJSON(w, map[string]interface{}{
		"lines":  content.Lines,
		"total":  content.TotalLines,
		"offset": offset,
		"count":  len(content.Lines),
	})
}

// handleConfigUpdate handles dynamic configuration updates
func (s *DashboardServer) handleConfigUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		QPS       int `json:"qps"`
		BatchSize int `json:"batchSize"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if s.onConfigUpdate == nil {
		http.Error(w, "Dynamic configuration not supported in this mode", http.StatusNotImplemented)
		return
	}

	// Logging
	s.logger.Printf("UPDATE CONFIG request: QPS=%d, BatchSize=%d", req.QPS, req.BatchSize)

	// Execute callback which propagates to Replicator -> FlowWriters
	if err := s.onConfigUpdate(req.QPS, req.BatchSize); err != nil {
		http.Error(w, fmt.Sprintf("Failed to update config: %v", err), http.StatusInternalServerError)
		return
	}

	// Update local config reference for display purposes
	s.cfg.Advanced.QPS = req.QPS
	s.cfg.Advanced.BatchSize = req.BatchSize

	writeJSON(w, map[string]string{"status": "ok", "message": "Configuration updated successfully"})
}

func (s *DashboardServer) currentSnapshot() state.Snapshot {
	s.snapshotMu.RLock()
	snap := s.snapshot
	s.snapshotMu.RUnlock()
	if snap.UpdatedAt.IsZero() {
		loaded, err := s.store.Load()
		if err == nil {
			snap = loaded
		}
	}

	return snap
}

func loadTemplates() (*template.Template, error) {
	layout := filepath.Join(templatesDir(), "layout.html")
	index := filepath.Join(templatesDir(), "index.html")
	return template.ParseFiles(layout, index)
}

func templatesDir() string {
	return "internal/web/templates"
}

func staticDir() string {
	return "internal/web/static"
}

func writeJSON(w http.ResponseWriter, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type syncSummaryResponse struct {
	Stage          string          `json:"stage"`
	StageMessage   string          `json:"stageMessage,omitempty"`
	StageUpdatedAt time.Time       `json:"stageUpdatedAt,omitempty"`
	Source         sourceInfo      `json:"source"`
	Target         targetInfo      `json:"target"`
	Checkpoint     checkpointInfo  `json:"checkpoint"`
	UpdatedAt      time.Time       `json:"updatedAt"`
	ConfigPath     string          `json:"configPath"`
	StateDir       string          `json:"stateDir"`
	StageDetails   []stageResponse `json:"stageDetails"`
}

type stageResponse struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Message   string    `json:"message,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

type sourceInfo struct {
	Type string `json:"type"`
	Addr string `json:"addr"`
}

type targetInfo struct {
	Type        string  `json:"type"`
	Addr        string  `json:"addr"`
	InitialKeys float64 `json:"initialKeys"`
}

type checkpointInfo struct {
	LastSavedAt time.Time `json:"lastSavedAt,omitempty"`
	File        string    `json:"file"`
	Enabled     bool      `json:"enabled"`
}

type syncProgressResponse struct {
	Phase               string          `json:"phase"`
	Percent             float64         `json:"percent"`
	SourceKeysEstimated float64         `json:"sourceKeysEstimated"`
	TargetKeysInitial   float64         `json:"targetKeysInitial"`
	TargetKeysCurrent   float64         `json:"targetKeysCurrent"`
	SyncedKeys          float64         `json:"syncedKeys"`
	FlowStats           []flowProgress  `json:"flowStats"`
	Incremental         incrementalInfo `json:"incremental"`
	UpdatedAt           time.Time       `json:"updatedAt"`
}

type flowProgress struct {
	FlowID       int       `json:"flowId"`
	State        string    `json:"state"`
	Message      string    `json:"message,omitempty"`
	ImportedKeys float64   `json:"importedKeys"`
	UpdatedAt    time.Time `json:"updatedAt,omitempty"`
}

type incrementalInfo struct {
	LSNCurrent float64 `json:"lsnCurrent"`
	LSNApplied float64 `json:"lsnApplied"`
	LagLsns    float64 `json:"lagLsns"`
	LagMillis  float64 `json:"lagMillis"`
}

type checkResponse struct {
	Status           string              `json:"status"`
	Mode             string              `json:"mode,omitempty"`
	Message          string              `json:"message,omitempty"`
	StartedAt        time.Time           `json:"startedAt,omitempty"`
	FinishedAt       time.Time           `json:"finishedAt,omitempty"`
	DurationSeconds  float64             `json:"durationSeconds,omitempty"`
	InconsistentKeys int                 `json:"inconsistentKeys,omitempty"`
	Samples          []state.CheckSample `json:"samples,omitempty"`
	ResultFile       string              `json:"resultFile,omitempty"`
	SummaryFile      string              `json:"summaryFile,omitempty"`
}

func buildSyncSummary(cfg *config.Config, snap state.Snapshot) syncSummaryResponse {
	stageName := snap.PipelineStatus
	var stageMsg string
	var stageUpdated time.Time
	if len(snap.Stages) > 0 {
		if latestName, latest := findLatestStage(snap.Stages); latestName != "" {
			stageName = latest.Status
			stageMsg = latest.Message
			stageUpdated = latest.UpdatedAt
		}
	}
	stageDetails := make([]stageResponse, 0, len(snap.Stages))
	for name, info := range snap.Stages {
		stageDetails = append(stageDetails, stageResponse{
			Name:      name,
			Status:    info.Status,
			Message:   info.Message,
			UpdatedAt: info.UpdatedAt,
		})
	}
	sort.Slice(stageDetails, func(i, j int) bool {
		return stageDetails[i].Name < stageDetails[j].Name
	})
	cpInfo := checkpointInfo{
		Enabled: cfg.Checkpoint.Enabled,
		File:    cfg.ResolveCheckpointPath(),
	}
	if ts, ok := snap.Metrics[state.MetricCheckpointSavedAtUnix]; ok && ts > 0 {
		cpInfo.LastSavedAt = time.Unix(int64(ts), 0)
	}
	return syncSummaryResponse{
		Stage:          defaultString(stageName, "idle"),
		StageMessage:   stageMsg,
		StageUpdatedAt: stageUpdated,
		Source: sourceInfo{
			Type: cfg.Source.Type,
			Addr: cfg.Source.Addr,
		},
		Target: targetInfo{
			Type:        cfg.Target.Type,
			Addr:        cfg.Target.Addr,
			InitialKeys: snap.Metrics[state.MetricTargetKeysInitial],
		},
		Checkpoint:   cpInfo,
		UpdatedAt:    snap.UpdatedAt,
		ConfigPath:   cfg.ConfigDir(),
		StateDir:     cfg.ResolveStateDir(),
		StageDetails: stageDetails,
	}
}

func buildSyncProgress(cfg *config.Config, snap state.Snapshot) syncProgressResponse {
	sourceKeys := snap.Metrics[state.MetricSourceKeysEstimated]
	targetInitial := snap.Metrics[state.MetricTargetKeysInitial]
	targetCurrent := snap.Metrics[state.MetricTargetKeysCurrent]
	synced := snap.Metrics[state.MetricSyncedKeys]
	if synced == 0 {
		synced = targetCurrent - targetInitial
	}
	percent := 0.0
	if sourceKeys > 0 {
		percent = clamp01(synced / sourceKeys)
	}
	return syncProgressResponse{
		Phase:               defaultString(snap.PipelineStatus, "idle"),
		Percent:             percent,
		SourceKeysEstimated: sourceKeys,
		TargetKeysInitial:   targetInitial,
		TargetKeysCurrent:   targetCurrent,
		SyncedKeys:          synced,
		FlowStats:           buildFlowStats(snap),
		Incremental:         buildIncrementalInfo(snap),
		UpdatedAt:           snap.UpdatedAt,
	}
}

func buildIncrementalInfo(snap state.Snapshot) incrementalInfo {
	current := snap.Metrics[state.MetricIncrementalLSNCurrent]
	applied := snap.Metrics[state.MetricIncrementalLSNApplied]
	lag := current - applied
	if lag < 0 {
		lag = 0
	}
	return incrementalInfo{
		LSNCurrent: current,
		LSNApplied: applied,
		LagLsns:    lag,
		LagMillis:  snap.Metrics[state.MetricIncrementalLagMs],
	}
}

func buildFlowStats(snap state.Snapshot) []flowProgress {
	var flows []flowProgress
	for name, info := range snap.Stages {
		if !strings.HasPrefix(name, "flow:") {
			continue
		}
		idStr := strings.TrimPrefix(name, "flow:")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			continue
		}
		metricKey := fmt.Sprintf(state.MetricFlowImportedFormat, id)
		flows = append(flows, flowProgress{
			FlowID:       id,
			State:        info.Status,
			Message:      info.Message,
			ImportedKeys: snap.Metrics[metricKey],
			UpdatedAt:    info.UpdatedAt,
		})
	}
	sort.Slice(flows, func(i, j int) bool {
		return flows[i].FlowID < flows[j].FlowID
	})
	return flows
}

func buildCheckResponse(snap state.Snapshot) checkResponse {
	if snap.Check == nil {
		return checkResponse{Status: "not_run"}
	}
	return checkResponse{
		Status:           snap.Check.Status,
		Mode:             snap.Check.Mode,
		Message:          snap.Check.Message,
		StartedAt:        snap.Check.StartedAt,
		FinishedAt:       snap.Check.FinishedAt,
		DurationSeconds:  snap.Check.DurationSeconds,
		InconsistentKeys: snap.Check.InconsistentKeys,
		Samples:          snap.Check.Samples,
		ResultFile:       snap.Check.ResultFile,
		SummaryFile:      snap.Check.SummaryFile,
	}
}

func findLatestStage(stages map[string]state.StageSnapshot) (string, state.StageSnapshot) {
	var latestName string
	var latest state.StageSnapshot
	for name, st := range stages {
		if st.UpdatedAt.After(latest.UpdatedAt) {
			latest = st
			latestName = name
		}
	}
	return latestName, latest
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

type logContent struct {
	Lines      []string
	TotalLines int
}

func readLogFile(path string, offset, count int) (*logContent, error) {
	// Open file
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		// Return empty content instead of an error when file is missing
		if os.IsNotExist(err) {
			return &logContent{Lines: []string{}, TotalLines: 0}, nil
		}
		return nil, err
	}

	// Split into lines
	content := string(data)
	allLines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(allLines) == 1 && allLines[0] == "" {
		allLines = []string{}
	}

	totalLines := len(allLines)

	// Compute slice window
	start := offset

	// Tail mode: calculate start based on count from end
	if offset == -1 {
		start = totalLines - count
		if start < 0 {
			start = 0
		}
	} else if start > totalLines {
		start = totalLines
	}

	end := start + count
	if end > totalLines {
		end = totalLines
	}

	// Extract requested lines
	lines := []string{}
	if start < end {
		lines = allLines[start:end]
	}

	return &logContent{
		Lines:      lines,
		TotalLines: totalLines,
	}, nil
}

// inferLogFilePath attempts to find the actual log file based on config and existing files.
// Log file naming format: {taskName}_{mode}.log or {sourceType}_{sourceAddr}_{mode}.log
func (s *DashboardServer) inferLogFilePath() string {
	logDir := s.cfg.Log.Dir

	// Try multiple possible log file patterns
	var candidates []string

	// Pattern 1: taskName_replicate.log (most common)
	if s.cfg.TaskName != "" {
		candidates = append(candidates,
			filepath.Join(logDir, fmt.Sprintf("%s_replicate.log", s.cfg.TaskName)),
			filepath.Join(logDir, fmt.Sprintf("%s_migrate.log", s.cfg.TaskName)),
			filepath.Join(logDir, fmt.Sprintf("%s_check.log", s.cfg.TaskName)),
		)
	}

	// Pattern 2: sourceType_sourceAddr_mode.log
	sourceType := s.cfg.Source.Type
	if sourceType == "" {
		sourceType = "dragonfly"
	}
	addr := strings.ReplaceAll(s.cfg.Source.Addr, ":", "_")
	addr = strings.ReplaceAll(addr, ".", "_")
	candidates = append(candidates,
		filepath.Join(logDir, fmt.Sprintf("%s_%s_replicate.log", sourceType, addr)),
		filepath.Join(logDir, fmt.Sprintf("%s_%s_migrate.log", sourceType, addr)),
	)

	// Pattern 3: fallback to default
	candidates = append(candidates, filepath.Join(logDir, "df2redis.log"))

	// Return first existing file (return absolute path for reliability)
	for _, candidate := range candidates {
		absPath, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if _, err := os.Stat(absPath); err == nil {
			s.logger.Printf("Found log file: %s", absPath)
			return absPath // Return absolute path
		}
	}

	// No file found, return first candidate as absolute path
	if len(candidates) > 0 {
		absPath, err := filepath.Abs(candidates[0])
		if err == nil {
			s.logger.Printf("No log file found, using fallback: %s", absPath)
			return absPath
		}
		return candidates[0]
	}

	return filepath.Join(s.cfg.Log.Dir, "df2redis.log")
}

// CheckStatus holds the current status of a validation task
type CheckStatus struct {
	Running          bool      `json:"running"`
	CompareMode      int       `json:"compareMode"`      // 1=full value, 2=length, 3=key outline, 4=smart
	CompareTimes     int       `json:"compareTimes"`     // number of comparison rounds
	Round            int       `json:"round"`            // current round index
	QPS              int       `json:"qps"`              // QPS limit
	StartedAt        time.Time `json:"startedAt"`        // start time
	Progress         float64   `json:"progress"`         // 0.0 - 1.0 overall progress
	RoundProgress    float64   `json:"roundProgress"`    // current round progress 0.0 - 1.0
	CheckedKeys      int64     `json:"checkedKeys"`      // number of checked keys
	TotalKeys        int64     `json:"totalKeys"`        // total number of keys
	ConsistentKeys   int64     `json:"consistentKeys"`   // count of consistent keys
	InconsistentKeys int64     `json:"inconsistentKeys"` // count of inconsistent keys
	ErrorCount       int64     `json:"errorCount"`       // error count
	Message          string    `json:"message"`          // status message
	ElapsedSeconds   float64   `json:"elapsedSeconds"`   // elapsed seconds
	EstimatedSeconds float64   `json:"estimatedSeconds"` // estimated total seconds
}
