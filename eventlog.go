package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type LogEntry struct {
	Timestamp float64 `json:"ts"`
	Event     string  `json:"event"`
	State     string  `json:"state"`
	ToolName  string  `json:"tool_name,omitempty"`
	Cwd       string  `json:"cwd,omitempty"`
	Detail    string  `json:"detail,omitempty"`
}

type EventLogger struct {
	logDir string
	mu     sync.Mutex
	files  map[string]*os.File
}

func NewEventLogger(logDir string) (*EventLogger, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, err
	}
	return &EventLogger{
		logDir: logDir,
		files:  make(map[string]*os.File),
	}, nil
}

func (el *EventLogger) Append(sessionID string, entry LogEntry) error {
	el.mu.Lock()
	defer el.mu.Unlock()

	f, err := el.getFile(sessionID)
	if err != nil {
		return err
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

func (el *EventLogger) ReadLog(sessionID string) ([]LogEntry, error) {
	path := filepath.Join(el.logDir, sessionID+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []LogEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry LogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, scanner.Err()
}

func (el *EventLogger) ListSessions() ([]string, error) {
	entries, err := os.ReadDir(el.logDir)
	if err != nil {
		return nil, err
	}

	var sessions []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			sessions = append(sessions, strings.TrimSuffix(e.Name(), ".jsonl"))
		}
	}
	return sessions, nil
}

func (el *EventLogger) Close() {
	el.mu.Lock()
	defer el.mu.Unlock()

	for _, f := range el.files {
		f.Close()
	}
	el.files = make(map[string]*os.File)
}

func (el *EventLogger) CloseSession(sessionID string) {
	el.mu.Lock()
	defer el.mu.Unlock()

	if f, ok := el.files[sessionID]; ok {
		f.Close()
		delete(el.files, sessionID)
	}
}

func (el *EventLogger) getFile(sessionID string) (*os.File, error) {
	if f, ok := el.files[sessionID]; ok {
		return f, nil
	}

	path := filepath.Join(el.logDir, sessionID+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	el.files[sessionID] = f
	return f, nil
}
