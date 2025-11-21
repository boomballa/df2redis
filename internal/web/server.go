package web

import (
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"path/filepath"
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
func (s *DashboardServer) Start() error {
	if s.addr == "" {
		s.addr = ":8080"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/status", s.handleStatus)
	fileServer := http.FileServer(http.Dir(staticDir()))
	mux.HandleFunc("/static/", func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/static")
		fileServer.ServeHTTP(w, r)
	})

	go s.refreshLoop()

	log.Printf("[dashboard] serving at http://%s", s.addr)
	return http.ListenAndServe(s.addr, mux)
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
	ctx := map[string]interface{}{
		"StateDir":    s.cfg.ResolveStateDir(),
		"StatusFile":  s.cfg.StatusFilePath(),
		"GeneratedAt": time.Now().Format(time.RFC3339),
		"Source":      s.cfg.Source,
		"Target":      s.cfg.Target,
		"Snapshot":    s.cfg.Migrate.SnapshotPath,
	}
	s.snapshotMu.RLock()
	ctx["Snapshot"] = s.snapshot
	s.snapshotMu.RUnlock()

	if err := s.tmpl.ExecuteTemplate(w, "index.html", ctx); err != nil {
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
