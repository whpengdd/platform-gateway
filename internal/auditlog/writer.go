package auditlog

import (
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const defaultQueue = 256

// Writer appends one JSONL line per request under dir/platform-gateway-YYYY-MM-DD.jsonl (Asia/Shanghai date).
// Logging never uses a database. A full buffer drops the line instead of blocking the request.
type Writer struct {
	dir  string
	loc  *time.Location
	ch   chan []byte
	done chan struct{}
	once sync.Once
}

func Shanghai() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return loc
}

func NewWriter(dir string) (*Writer, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	w := &Writer{
		dir:  dir,
		loc:  Shanghai(),
		ch:   make(chan []byte, defaultQueue),
		done: make(chan struct{}),
	}
	go w.loop()
	return w, nil
}

func (w *Writer) Enabled() bool { return w != nil }

func (w *Writer) Emit(rec *Record) {
	if w == nil || rec == nil {
		return
	}
	line := rec.Marshal()
	if len(line) == 0 {
		return
	}
	select {
	case w.ch <- line:
	default:
		log.Printf("auditlog: drop line, buffer full")
	}
}

func (w *Writer) Close() {
	if w == nil {
		return
	}
	w.once.Do(func() {
		close(w.ch)
		<-w.done
	})
}

func (w *Writer) loop() {
	defer close(w.done)
	var (
		curDay string
		f      *os.File
	)
	closeFile := func() {
		if f != nil {
			_ = f.Sync()
			_ = f.Close()
			f = nil
			curDay = ""
		}
	}
	defer closeFile()
	for line := range w.ch {
		day := time.Now().In(w.loc).Format("2006-01-02")
		if f == nil || day != curDay {
			closeFile()
			path := filepath.Join(w.dir, "platform-gateway-"+day+".jsonl")
			opened, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
			if err != nil {
				log.Printf("auditlog: open %s: %v", path, err)
				continue
			}
			f = opened
			curDay = day
		}
		if _, err := f.Write(line); err != nil {
			log.Printf("auditlog: write: %v", err)
			closeFile()
		}
	}
}
