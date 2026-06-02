package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
)

type SessionFile struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	Cwd        string `json:"cwd"`
	StartedAt  int64  `json:"startedAt"`
	Kind       string `json:"kind"`
	Entrypoint string `json:"entrypoint"`
}

func ScanSessions(sessionsDir string) ([]SessionFile, error) {
	pattern := filepath.Join(sessionsDir, "*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}

	var sessions []SessionFile
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var s SessionFile
		if err := json.Unmarshal(data, &s); err != nil {
			continue
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

func CheckPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func FindDeadPIDs(sessions []SessionFile) map[int]bool {
	dead := make(map[int]bool)
	for _, s := range sessions {
		if !CheckPIDAlive(s.PID) {
			dead[s.PID] = true
		}
	}
	return dead
}
