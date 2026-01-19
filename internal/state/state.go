package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// StageSnapshot holds state per pipeline stage.
type StageSnapshot struct {
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Event represents timeline records.
type Event struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
}

// Snapshot is the persisted status structure.
type Snapshot struct {
	PipelineStatus string                   `json:"pipelineStatus"`
	Stages         map[string]StageSnapshot `json:"stages"`
	Metrics        map[string]float64       `json:"metrics"`
	Events         []Event                  `json:"events"`
	Check          *CheckResult             `json:"check,omitempty"`
	UpdatedAt      time.Time                `json:"updatedAt"`
}

// CheckSample captures an inconsistent key found during validation.
type CheckSample struct {
	Key    string `json:"key"`
	Source string `json:"source,omitempty"`
	Target string `json:"target,omitempty"`
}

// CheckResult stores latest redis-full-check run details.
type CheckResult struct {
	Status           string        `json:"status"`
	Mode             string        `json:"mode,omitempty"`
	Message          string        `json:"message,omitempty"`
	StartedAt        time.Time     `json:"startedAt,omitempty"`
	FinishedAt       time.Time     `json:"finishedAt,omitempty"`
	DurationSeconds  float64       `json:"durationSeconds,omitempty"`
	InconsistentKeys int           `json:"inconsistentKeys,omitempty"`
	Samples          []CheckSample `json:"samples,omitempty"`
	ResultFile       string        `json:"resultFile,omitempty"`
	SummaryFile      string        `json:"summaryFile,omitempty"`
}

// Store persists snapshot to disk.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore returns new state store.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Load returns current snapshot if exists.
func (s *Store) Load() (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// load reads the snapshot from disk without locking (internal use).
func (s *Store) load() (Snapshot, error) {
	var snap Snapshot
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return Snapshot{
				PipelineStatus: "idle",
				Stages:         map[string]StageSnapshot{},
				Metrics:        map[string]float64{},
				Events:         []Event{},
				UpdatedAt:      time.Now(),
			}, nil
		}
		return snap, err
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		return snap, err
	}
	return snap, nil
}

// Write persists snapshot.
func (s *Store) Write(snap Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(snap)
}

// write persists snapshot to disk without locking (internal use).
func (s *Store) write(snap Snapshot) error {
	snap.UpdatedAt = time.Now()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	temp := s.path + ".tmp"
	if err := os.WriteFile(temp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(temp, s.path)
}

// UpdateStage records stage status.
func (s *Store) UpdateStage(name string, status string, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap, err := s.load()
	if err != nil {
		return err
	}
	if snap.Stages == nil {
		snap.Stages = map[string]StageSnapshot{}
	}
	snap.Stages[name] = StageSnapshot{
		Status:    status,
		Message:   message,
		UpdatedAt: time.Now(),
	}
	return s.write(snap)
}

// SetPipelineStatus updates overall pipeline status.
func (s *Store) SetPipelineStatus(status string, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap, err := s.load()
	if err != nil {
		return err
	}
	snap.PipelineStatus = status
	if message != "" {
		snap.Events = append(snap.Events, Event{
			Timestamp: time.Now(),
			Type:      status,
			Message:   message,
		})
	}
	return s.write(snap)
}

// RecordMetric stores numeric metrics.
func (s *Store) RecordMetric(name string, value float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap, err := s.load()
	if err != nil {
		return err
	}
	if snap.Metrics == nil {
		snap.Metrics = map[string]float64{}
	}
	snap.Metrics[name] = value
	return s.write(snap)
}

// UpdateMetrics merges a batch of metric values into the snapshot.
func (s *Store) UpdateMetrics(updates map[string]float64) error {
	if len(updates) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	snap, err := s.load()
	if err != nil {
		return err
	}
	if snap.Metrics == nil {
		snap.Metrics = map[string]float64{}
	}
	for k, v := range updates {
		snap.Metrics[k] = v
	}
	return s.write(snap)
}

// SaveCheckResult records the latest data validation result.
func (s *Store) SaveCheckResult(res CheckResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap, err := s.load()
	if err != nil {
		return err
	}
	snap.Check = &res
	return s.write(snap)
}
