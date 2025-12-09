package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
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
}

// Options configure the dashboard server.
type Options struct {
	Addr  string
	Cfg   *config.Config
	Store *state.Store
}

// New creates a dashboard server.
func New(opts Options) (*DashboardServer, error) {
	tmpl, err := loadTemplates()
	if err != nil {
		return nil, err
	}
	return &DashboardServer{
		addr:  opts.Addr,
		cfg:   opts.Cfg,
		store: opts.Store,
		tmpl:  tmpl,
	}, nil
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
	fileServer := http.FileServer(http.Dir(staticDir()))
	mux.HandleFunc("/static/", func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/static")
		fileServer.ServeHTTP(w, r)
	})

	go s.refreshLoop()

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	actualAddr := ln.Addr().String()
	s.addr = actualAddr
	if ready != nil {
		ready <- actualAddr
	}
	log.Printf("[dashboard] serving at http://%s", actualAddr)
	server := &http.Server{Handler: mux}
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
	logFile := filepath.Join(s.cfg.Log.Dir, "df2redis.log")

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
	snap, err := s.store.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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
	// 解析查询参数
	linesParam := r.URL.Query().Get("lines")
	offsetParam := r.URL.Query().Get("offset")

	lines := 100 // 默认 100 行
	if linesParam != "" {
		if parsed, err := strconv.Atoi(linesParam); err == nil && parsed > 0 {
			lines = parsed
		}
	}

	offset := 0 // 默认从头开始
	if offsetParam != "" {
		if parsed, err := strconv.Atoi(offsetParam); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	// 构建日志文件路径
	logPath := filepath.Join(s.cfg.Log.Dir, "df2redis.log")

	// 读取日志文件
	content, err := readLogFile(logPath, offset, lines)
	if err != nil {
		writeJSON(w, map[string]interface{}{
			"error": fmt.Sprintf("读取日志失败: %v", err),
			"lines": []string{},
			"total": 0,
			"offset": offset,
		})
		return
	}

	writeJSON(w, map[string]interface{}{
		"lines":  content.Lines,
		"total":  content.TotalLines,
		"offset": offset,
		"count":  len(content.Lines),
	})
}

func (s *DashboardServer) currentSnapshot() state.Snapshot {
	s.snapshotMu.RLock()
	snap := s.snapshot
	s.snapshotMu.RUnlock()
	if snap.UpdatedAt.IsZero() {
		loaded, err := s.store.Load()
		if err == nil {
			return loaded
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
	Seed        string  `json:"seed"`
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
			Seed:        cfg.Target.Seed,
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
	// 打开文件
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		// 如果文件不存在，返回空内容而不是错误
		if os.IsNotExist(err) {
			return &logContent{Lines: []string{}, TotalLines: 0}, nil
		}
		return nil, err
	}

	// 按行分割
	content := string(data)
	allLines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(allLines) == 1 && allLines[0] == "" {
		allLines = []string{}
	}

	totalLines := len(allLines)

	// 计算切片范围
	start := offset
	if start > totalLines {
		start = totalLines
	}

	end := start + count
	if end > totalLines {
		end = totalLines
	}

	// 提取请求的行
	lines := []string{}
	if start < end {
		lines = allLines[start:end]
	}

	return &logContent{
		Lines:      lines,
		TotalLines: totalLines,
	}, nil
}
