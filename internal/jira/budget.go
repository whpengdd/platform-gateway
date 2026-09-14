package jira

import (
	"sync"
	"time"
)

type budget struct {
	mu                     sync.Mutex
	minute, day            int64
	reads, writes, creates int
	bytes                  int64
}

func (b *budget) take(now time.Time, write, create bool, bytes int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	minute := now.Unix() / 60
	day := now.Unix() / 86400
	if b.minute != minute {
		b.minute = minute
		b.reads = 0
		b.writes = 0
	}
	if b.day != day {
		b.day = day
		b.creates = 0
		b.bytes = 0
	}
	if write && b.writes >= 20 || !write && b.reads >= 60 || create && b.creates >= 100 || b.bytes+bytes > 200<<20 {
		return false
	}
	if write {
		b.writes++
	} else {
		b.reads++
	}
	if create {
		b.creates++
	}
	b.bytes += bytes
	return true
}
