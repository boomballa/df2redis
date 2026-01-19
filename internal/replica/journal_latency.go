package replica

import "time"

// addJournalLatency records latency for a single journal operation
func (r *Replicator) addJournalLatency(d time.Duration) {
	ms := float64(d.Microseconds()) / 1000.0
	r.journalPerf.mu.Lock()
	r.journalPerf.latencies = append(r.journalPerf.latencies, ms)
	r.journalPerf.mu.Unlock()
}
