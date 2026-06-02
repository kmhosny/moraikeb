package main

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	StateStarting    = "starting"
	StateThinking    = "thinking"
	StateToolCalling = "tool_calling"
	StateWaiting     = "waiting"
	StateIdle        = "idle"
	StateDone        = "done"
)

type AgentInfo struct {
	SessionID   string  `json:"session_id"`
	PID         int     `json:"pid,omitempty"`
	Cwd         string  `json:"cwd"`
	State       string  `json:"state"`
	StateSince  float64 `json:"state_since"`
	StartedAt   float64 `json:"started_at,omitempty"`
	CurrentTool string  `json:"current_tool,omitempty"`
	LastTool    string  `json:"last_tool,omitempty"`
	LastPrompt  string  `json:"last_prompt,omitempty"`
	Kind        string  `json:"kind,omitempty"`
	Entrypoint  string  `json:"entrypoint,omitempty"`
}

type HookEvent struct {
	SessionID     string          `json:"session_id"`
	HookEventName string          `json:"hook_event_name"`
	Cwd           string          `json:"cwd"`
	ToolName      string          `json:"tool_name,omitempty"`
	ToolInput     json.RawMessage `json:"tool_input,omitempty"`
	ToolResult    json.RawMessage `json:"tool_result,omitempty"`
	UserPrompt    string          `json:"user_prompt,omitempty"`
	Reason        string          `json:"reason,omitempty"`
	Matcher       string          `json:"matcher,omitempty"`
}

type StateManager struct {
	mu       sync.RWMutex
	agents   map[string]*AgentInfo
	clientMu sync.Mutex
	clients  map[*websocket.Conn]bool
	eventLog *EventLogger
}

func NewStateManager(eventLog *EventLogger) *StateManager {
	return &StateManager{
		agents:   make(map[string]*AgentInfo),
		clients:  make(map[*websocket.Conn]bool),
		eventLog: eventLog,
	}
}

func (sm *StateManager) HandleHook(evt HookEvent) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	agent, exists := sm.agents[evt.SessionID]
	if !exists {
		agent = &AgentInfo{
			SessionID:  evt.SessionID,
			Cwd:        evt.Cwd,
			State:      StateStarting,
			StateSince: nowUnix(),
		}
		sm.agents[evt.SessionID] = agent
	}

	if evt.Cwd != "" && agent.Cwd == "" {
		agent.Cwd = evt.Cwd
	}

	oldState := agent.State
	var detail string

	switch evt.HookEventName {
	case "SessionStart":
		agent.State = StateStarting
		detail = "session started"

	case "UserPromptSubmit":
		agent.State = StateThinking
		prompt := evt.UserPrompt
		if len(prompt) > 100 {
			prompt = prompt[:100] + "..."
		}
		agent.LastPrompt = prompt
		detail = prompt

	case "PreToolUse":
		agent.State = StateToolCalling
		agent.CurrentTool = evt.ToolName
		detail = evt.ToolName + ": " + truncateToolInput(evt.ToolInput)

	case "PostToolUse":
		agent.State = StateThinking
		agent.LastTool = evt.ToolName
		agent.CurrentTool = ""
		detail = evt.ToolName + " completed"

	case "Notification":
		matcher := evt.ToolName
		if matcher == "" {
			matcher = evt.Matcher
		}
		switch matcher {
		case "permission_prompt":
			agent.State = StateWaiting
			detail = "waiting for permission"
		case "idle_prompt":
			agent.State = StateIdle
			detail = "idle"
		default:
			detail = "notification: " + matcher
		}

	case "Stop":
		agent.State = StateDone
		detail = evt.Reason
		if detail == "" {
			detail = "agent stopped"
		}

	case "SubagentStop":
		detail = "subagent stopped"
		if evt.Reason != "" {
			detail += ": " + evt.Reason
		}

	case "SessionEnd":
		agent.State = StateDone
		detail = "session ended"
	}

	if agent.State != oldState {
		agent.StateSince = nowUnix()
	}

	// Log the event
	if sm.eventLog != nil {
		entry := LogEntry{
			Timestamp: nowUnix(),
			Event:     evt.HookEventName,
			State:     agent.State,
			ToolName:  evt.ToolName,
			Cwd:       evt.Cwd,
			Detail:    detail,
		}
		if err := sm.eventLog.Append(evt.SessionID, entry); err != nil {
			log.Printf("event log error: %v", err)
		}
	}

	// Broadcast update
	sm.broadcast(map[string]interface{}{
		"type":  "agent_update",
		"agent": agent,
	})
}

func (sm *StateManager) GetSnapshot() []AgentInfo {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	agents := make([]AgentInfo, 0, len(sm.agents))
	for _, a := range sm.agents {
		agents = append(agents, *a)
	}
	return agents
}

func (sm *StateManager) AddClient(conn *websocket.Conn) {
	sm.clientMu.Lock()
	defer sm.clientMu.Unlock()
	sm.clients[conn] = true
}

func (sm *StateManager) RemoveClient(conn *websocket.Conn) {
	sm.clientMu.Lock()
	defer sm.clientMu.Unlock()
	delete(sm.clients, conn)
}

func (sm *StateManager) broadcast(msg interface{}) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("broadcast marshal error: %v", err)
		return
	}

	sm.clientMu.Lock()
	defer sm.clientMu.Unlock()

	for conn := range sm.clients {
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			conn.Close()
			delete(sm.clients, conn)
		}
	}
}

func (sm *StateManager) Reconcile(sessions []SessionFile) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for _, s := range sessions {
		agent, exists := sm.agents[s.SessionID]
		if !exists {
			agent = &AgentInfo{
				SessionID:  s.SessionID,
				Cwd:        s.Cwd,
				State:      StateStarting,
				StateSince: nowUnix(),
			}
			sm.agents[s.SessionID] = agent
		}
		agent.PID = s.PID
		if s.StartedAt > 0 {
			agent.StartedAt = float64(s.StartedAt) / 1000.0
		}
		agent.Kind = s.Kind
		agent.Entrypoint = s.Entrypoint
	}
}

func (sm *StateManager) CleanupStale(deadPIDs map[int]bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := nowUnix()
	var toRemove []string

	for id, agent := range sm.agents {
		// Mark agents with dead PIDs as done
		if agent.PID > 0 && deadPIDs[agent.PID] && agent.State != StateDone {
			agent.State = StateDone
			agent.StateSince = now
			sm.broadcast(map[string]interface{}{
				"type":  "agent_update",
				"agent": agent,
			})
		}
		// Prune agents done for more than 1 hour
		if agent.State == StateDone && (now-agent.StateSince) > 3600 {
			toRemove = append(toRemove, id)
		}
	}

	for _, id := range toRemove {
		delete(sm.agents, id)
		sm.broadcast(map[string]interface{}{
			"type":       "agent_remove",
			"session_id": id,
		})
	}
}

func nowUnix() float64 {
	return float64(time.Now().UnixMilli()) / 1000.0
}

func truncateToolInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		s := string(raw)
		if len(s) > 80 {
			return s[:80] + "..."
		}
		return s
	}
	// Try to extract a useful summary
	if cmd, ok := m["command"].(string); ok {
		if len(cmd) > 80 {
			return cmd[:80] + "..."
		}
		return cmd
	}
	if fp, ok := m["file_path"].(string); ok {
		return fp
	}
	if pattern, ok := m["pattern"].(string); ok {
		return pattern
	}
	s := string(raw)
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}
