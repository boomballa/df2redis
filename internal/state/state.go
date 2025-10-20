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
	UpdatedAt      time.Time                `json:"updatedAt"`
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
	snap, err := s.Load()
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
	return s.Write(snap)
}

// SetPipelineStatus updates overall pipeline status.
func (s *Store) SetPipelineStatus(status string, message string) error {
	snap, err := s.Load()
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
	return s.Write(snap)
}

// RecordMetric stores numeric metrics.
func (s *Store) RecordMetric(name string, value float64) error {
	snap, err := s.Load()
	if err != nil {
		return err
	}
	if snap.Metrics == nil {
		snap.Metrics = map[string]float64{}
	}
	snap.Metrics[name] = value
	return s.Write(snap)
}
