package replica

import "sync"

// RDBEntry pool for object reuse to reduce GC pressure
var entryPool = sync.Pool{
	New: func() interface{} {
		return &RDBEntry{}
	},
}

// GetRDBEntry gets an entry from the pool
func GetRDBEntry() *RDBEntry {
	return entryPool.Get().(*RDBEntry)
}

// PutRDBEntry returns an entry to the pool
func PutRDBEntry(entry *RDBEntry) {
	// Reset the entry before returning to pool
	entry.Key = ""
	entry.Type = 0
	entry.Value = nil
	entry.ExpireMs = 0
	entry.DbIndex = 0
	entryPool.Put(entry)
}
