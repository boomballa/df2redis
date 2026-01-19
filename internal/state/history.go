package state

import (
	"sync"
	"time"
)

// DataPoint represents a single point in time for a metric
type DataPoint struct {
	Timestamp int64   `json:"ts"` // Unix timestamp in milliseconds
	Value     float64 `json:"v"`
}

// TimeSeries acts as a circular buffer for metric history
type TimeSeries struct {
	Points []DataPoint `json:"points"`
	size   int
	head   int // Index where the next item will be written
	full   bool
	mu     sync.RWMutex
}

// NewTimeSeries creates a new history buffer of fixed size
func NewTimeSeries(size int) *TimeSeries {
	return &TimeSeries{
		Points: make([]DataPoint, size),
		size:   size,
	}
}

// Add appends a new value to the series
func (ts *TimeSeries) Add(val float64) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	ts.Points[ts.head] = DataPoint{
		Timestamp: time.Now().UnixMilli(),
		Value:     val,
	}
	ts.head = (ts.head + 1) % ts.size
	if ts.head == 0 {
		ts.full = true
	}
}

// Snapshot returns all valid points in chronological order
func (ts *TimeSeries) Snapshot() []DataPoint {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	if !ts.full && ts.head == 0 {
		return []DataPoint{}
	}

	result := make([]DataPoint, 0, ts.size)

	if ts.full {
		// Read from older (head) to end
		result = append(result, ts.Points[ts.head:]...)
		// Read from start to head
		result = append(result, ts.Points[:ts.head]...)
	} else {
		// Buffer not full, just read from 0 to head
		result = append(result, ts.Points[:ts.head]...)
	}
	return result
}

// HistoryStore holds buffers for all tracked metrics
type HistoryStore struct {
	QPS        *TimeSeries `json:"qps"`
	LatencyP50 *TimeSeries `json:"latency_p50"`
	LatencyP95 *TimeSeries `json:"latency_p95"`
	LatencyP99 *TimeSeries `json:"latency_p99"`
}

// NewHistoryStore creates storage for 1 hour of data at 1s resolution
func NewHistoryStore() *HistoryStore {
	const OneHour = 3600
	return &HistoryStore{
		QPS:        NewTimeSeries(OneHour),
		LatencyP50: NewTimeSeries(OneHour),
		LatencyP95: NewTimeSeries(OneHour),
		LatencyP99: NewTimeSeries(OneHour),
	}
}
