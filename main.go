package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	port := flag.Int("port", 7422, "HTTP server port")
	install := flag.Bool("install", false, "Install hooks into ~/.claude/settings.json")
	flag.Parse()

	if *install {
		// Get absolute path to hook.sh
		exePath, err := os.Executable()
		if err != nil {
			// Fallback: use working directory
			wd, _ := os.Getwd()
			exePath = filepath.Join(wd, "hook.sh")
		} else {
			exePath = filepath.Join(filepath.Dir(exePath), "hook.sh")
		}
		// If running via go run, use working directory
		if _, err := os.Stat(exePath); err != nil {
			wd, _ := os.Getwd()
			exePath = filepath.Join(wd, "hook.sh")
		}
		if err := installHooks(exePath); err != nil {
			log.Fatalf("install failed: %v", err)
		}
		return
	}

	// Make hook.sh executable
	wd, _ := os.Getwd()
	os.Chmod(filepath.Join(wd, "hook.sh"), 0755)

	// Set up event logger
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("get home dir: %v", err)
	}
	logDir := filepath.Join(homeDir, ".agent-monitoring", "logs")
	eventLog, err := NewEventLogger(logDir)
	if err != nil {
		log.Fatalf("event logger: %v", err)
	}
	defer eventLog.Close()

	// Set up state manager
	sm := NewStateManager(eventLog)

	// Set up server
	srv := NewServer(sm, eventLog)
	mux := http.NewServeMux()
	srv.SetupRoutes(mux)

	// Session scanner goroutine
	sessionsDir := filepath.Join(homeDir, ".claude", "sessions")
	go func() {
		// Run immediately on startup
		runScan(sm, sessionsDir)
		// Then every 10 seconds
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			runScan(sm, sessionsDir)
		}
	}()

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("Agent Monitor listening on http://%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func runScan(sm *StateManager, sessionsDir string) {
	sessions, err := ScanSessions(sessionsDir)
	if err != nil {
		log.Printf("scan error: %v", err)
		return
	}
	deadPIDs := FindDeadPIDs(sessions)
	sm.Reconcile(sessions)
	sm.CleanupStale(deadPIDs)
}
