package replica

import (
	"fmt"
	"sync"
	"time"

	"df2redis/internal/state"
)

type metricsRecorder struct {
	store   *state.Store
	mu      sync.Mutex
	pending map[string]float64
	ticker  *time.Ticker
	stopCh  chan struct{}
}

func newMetricsRecorder(store *state.Store) *metricsRecorder {
	if store == nil {
		return nil
	}
	m := &metricsRecorder{
		store:   store,
		pending: make(map[string]float64),
		ticker:  time.NewTicker(2 * time.Second),
		stopCh:  make(chan struct{}),
	}
	go m.loop()
	return m
}

func (m *metricsRecorder) loop() {
	for {
		select {
		case <-m.ticker.C:
			m.flush()
		case <-m.stopCh:
			m.flush()
			return
		}
	}
}

func (m *metricsRecorder) flush() {
	m.mu.Lock()
	if len(m.pending) == 0 {
		m.mu.Unlock()
		return
	}
	batch := make(map[string]float64, len(m.pending))
	for k, v := range m.pending {
		batch[k] = v
	}
	m.pending = make(map[string]float64)
	m.mu.Unlock()
	_ = m.store.UpdateMetrics(batch)
}

func (m *metricsRecorder) Close() {
	if m == nil {
		return
	}
	m.ticker.Stop()
	close(m.stopCh)
}

func (m *metricsRecorder) Set(name string, value float64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.pending[name] = value
	m.mu.Unlock()
}

func (m *metricsRecorder) SetFlowImported(flowID int, value float64) {
	if m == nil {
		return
	}
	m.Set(fmt.Sprintf(state.MetricFlowImportedFormat, flowID), value)
}
