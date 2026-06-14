package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type ActivityEvent struct {
	Time    string            `json:"time"`
	Level   string            `json:"level"`
	Type    string            `json:"type"`
	Host    string            `json:"host,omitempty"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

type ActivityLog struct {
	mu      sync.Mutex
	dir     string
	limit   int
	entries []ActivityEvent
}

func NewActivityLog(dir string, limit int) (*ActivityLog, error) {
	if limit <= 0 {
		limit = 500
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create activity log directory failed: %w", err)
	}
	log := &ActivityLog{dir: dir, limit: limit}
	log.loadRecent()
	return log, nil
}

func (l *ActivityLog) Add(level, typ, host, message string, details map[string]string) {
	if l == nil {
		return
	}
	event := ActivityEvent{
		Time:    time.Now().Format("2006-01-02 15:04:05"),
		Level:   level,
		Type:    typ,
		Host:    host,
		Message: message,
		Details: details,
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, event)
	if len(l.entries) > l.limit {
		l.entries = l.entries[len(l.entries)-l.limit:]
	}
	l.writeLocked(event)
}

func (l *ActivityLog) List(limit int) []ActivityEvent {
	if l == nil {
		return []ActivityEvent{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit <= 0 || limit > len(l.entries) {
		limit = len(l.entries)
	}
	out := make([]ActivityEvent, 0, limit)
	for i := len(l.entries) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, l.entries[i])
	}
	return out
}

func (l *ActivityLog) loadRecent() {
	files, err := filepath.Glob(filepath.Join(l.dir, "activity_*.log"))
	if err != nil || len(files) == 0 {
		return
	}
	sort.Strings(files)
	if len(files) > 3 {
		files = files[len(files)-3:]
	}
	for _, path := range files {
		l.loadFile(path)
	}
	if len(l.entries) > l.limit {
		l.entries = l.entries[len(l.entries)-l.limit:]
	}
}

func (l *ActivityLog) loadFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event ActivityEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err == nil && event.Time != "" {
			l.entries = append(l.entries, event)
		}
	}
}

func (l *ActivityLog) writeLocked(event ActivityEvent) {
	path := filepath.Join(l.dir, "activity_"+time.Now().Format("2006-01-02")+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = f.Write(append(data, '\n'))
}
