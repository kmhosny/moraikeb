package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
)

//go:embed ui/*
var uiFS embed.FS

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type Server struct {
	sm       *StateManager
	eventLog *EventLogger
}

func NewServer(sm *StateManager, eventLog *EventLogger) *Server {
	return &Server{sm: sm, eventLog: eventLog}
}

func (s *Server) SetupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/hook", s.handleHook)
	mux.HandleFunc("/ws", s.handleWebSocket)
	mux.HandleFunc("/api/sessions", s.handleListSessions)
	mux.HandleFunc("/api/sessions/", s.handleSessionEvents)

	// Static files from embedded UI
	uiContent, _ := fs.Sub(uiFS, "ui")
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			path = "index.html"
		} else {
			path = strings.TrimPrefix(path, "/")
		}
		data, err := fs.ReadFile(uiContent, path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		// Set content type
		switch {
		case strings.HasSuffix(path, ".html"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case strings.HasSuffix(path, ".css"):
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		case strings.HasSuffix(path, ".js"):
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		}
		w.Write(data)
	})
}

func (s *Server) handleHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var evt HookEvent
	if err := json.NewDecoder(r.Body).Decode(&evt); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	if evt.SessionID == "" {
		http.Error(w, "missing session_id", http.StatusBadRequest)
		return
	}

	log.Printf("hook: session=%s event=%s tool=%s", evt.SessionID[:min(8, len(evt.SessionID))], evt.HookEventName, evt.ToolName)
	s.sm.HandleHook(evt)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade error: %v", err)
		return
	}
	defer conn.Close()

	// Send current snapshot
	snapshot := s.sm.GetSnapshot()
	msg, _ := json.Marshal(map[string]interface{}{
		"type":   "snapshot",
		"agents": snapshot,
	})
	if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		return
	}

	// Register for broadcasts
	s.sm.AddClient(conn)
	defer s.sm.RemoveClient(conn)

	// Read loop (drain messages, detect disconnect)
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.eventLog.ListSessions()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"sessions": sessions,
	})
}

func (s *Server) handleSessionEvents(w http.ResponseWriter, r *http.Request) {
	// Extract session ID from /api/sessions/{id}/events
	path := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}
	sessionID := parts[0]

	entries, err := s.eventLog.ReadLog(sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"session_id": sessionID,
		"events":     entries,
	})
}
