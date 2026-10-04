package tools

import (
	"bytes"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

const maxOutputBufferSize = 1 * 1024 * 1024 // 1MB

const outputTruncateMarker = "\n... [output truncated, exceeded 1MB]\n"

// PtyKeyMode represents arrow key encoding mode for PTY sessions.
// Programs send smkx/rmkx sequences to switch between CSI and SS3 modes.
type PtyKeyMode uint8

const (
	PtyKeyModeCSI PtyKeyMode = iota // triggered by rmkx (\x1b[?1l)
	PtyKeyModeSS3                   // triggered by smkx (\x1b[?1h)
)

const PtyKeyModeNotFound PtyKeyMode = 255

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrSessionDone     = errors.New("session already completed")
	ErrPTYNotSupported = errors.New("PTY is not supported on this platform")
	ErrNoStdin         = errors.New("no stdin available")
)

type ProcessSession struct {
	mu              sync.Mutex
	ID              string
	PID             int
	Command         string
	PTY             bool
	Background      bool
	StartTime       int64
	ExitCode        int
	Status          string
	stdinWriter     io.Writer
	stdoutPipe      io.Reader
	outputBuffer    *bytes.Buffer
	outputTruncated bool
	ptyMaster       *os.File
	// terminator, when set, kills the whole process tree (job object on
	// Windows); nil falls back to the pid-based killProcessGroup.
	terminator func() error
	// finishedAt records when the process was reaped; cleanupOldSessions
	// keys on it so long-running tasks keep their output for the promised
	// window after exit (StartTime alone would delete a 40-minute task
	// minutes after it finished).
	finishedAt int64

	// ptyKeyMode tracks arrow key encoding mode (CSI vs SS3)
	ptyKeyMode PtyKeyMode
}

func (s *ProcessSession) IsDone() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Status == "done" || s.Status == "exited"
}

func (s *ProcessSession) GetPtyKeyMode() PtyKeyMode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ptyKeyMode
}

func (s *ProcessSession) SetPtyKeyMode(mode PtyKeyMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ptyKeyMode = mode
}

func (s *ProcessSession) GetStatus() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Status
}

func (s *ProcessSession) SetStatus(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = status
}

func (s *ProcessSession) GetExitCode() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ExitCode
}

func (s *ProcessSession) SetExitCode(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ExitCode = code
}

func (s *ProcessSession) killProcess() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Status != "running" {
		return ErrSessionDone
	}

	pid := s.PID
	if pid <= 0 {
		return ErrSessionNotFound
	}

	// Prefer the session's own tree terminator (job object on Windows): a
	// pid-chain walk misses orphaned grandchildren whose intermediate parent
	// already exited.
	if s.terminator != nil {
		if err := s.terminator(); err != nil {
			return err
		}
	} else if err := killProcessGroup(pid); err != nil {
		return err
	}

	s.Status = "done"
	s.ExitCode = -1
	return nil
}

func (s *ProcessSession) Kill() error {
	return s.killProcess()
}

func (s *ProcessSession) Write(data string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Status != "running" {
		return ErrSessionDone
	}

	var writer io.Writer
	if s.PTY && s.ptyMaster != nil {
		writer = s.ptyMaster
	} else if s.stdinWriter != nil {
		writer = s.stdinWriter
	} else {
		return ErrNoStdin
	}

	_, err := writer.Write([]byte(data))
	return err
}

func (s *ProcessSession) Read() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.outputBuffer.Len() == 0 {
		return ""
	}

	data := s.outputBuffer.String()
	s.outputBuffer.Reset()
	return data
}

func (s *ProcessSession) ToSessionInfo() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	return SessionInfo{
		ID:        s.ID,
		Command:   s.Command,
		Status:    s.Status,
		PID:       s.PID,
		StartedAt: s.StartTime,
	}
}

type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*ProcessSession
	stopCh   chan struct{}
	stopOnce sync.Once
}

func NewSessionManager() *SessionManager {
	sm := &SessionManager{
		sessions: make(map[string]*ProcessSession),
		stopCh:   make(chan struct{}),
	}

	// Start cleaner goroutine - runs every 5 minutes, cleans up sessions done for >30 minutes
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-sm.stopCh:
				return
			case <-ticker.C:
				sm.cleanupOldSessions()
			}
		}
	}()

	return sm
}

// Stop shuts down the background cleanup goroutine. Safe to call multiple
// times from concurrent goroutines. After Stop returns, the SessionManager
// is still usable — only the cleanup goroutine is terminated.
func (sm *SessionManager) Stop() {
	sm.stopOnce.Do(func() {
		close(sm.stopCh)
	})
}

// cleanupOldSessions removes sessions that are done and whose process
// finished more than 30 minutes ago (keyed on the exit time, not StartTime —
// a task that ran for hours must still keep its output for the promised
// window after it exits).
func (sm *SessionManager) cleanupOldSessions() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	cutoff := time.Now().Add(-30 * time.Minute)
	for id, session := range sm.sessions {
		if !session.IsDone() {
			continue
		}
		session.mu.Lock()
		finished := session.finishedAt
		session.mu.Unlock()
		if finished == 0 {
			finished = session.StartTime
		}
		if finished < cutoff.Unix() {
			delete(sm.sessions, id)
		}
	}
}

func (sm *SessionManager) Add(session *ProcessSession) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.sessions[session.ID] = session
}

func (sm *SessionManager) Get(sessionID string) (*ProcessSession, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, ok := sm.sessions[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}

	return session, nil
}

func (sm *SessionManager) Remove(sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.sessions, sessionID)
}

func (sm *SessionManager) List() []SessionInfo {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]SessionInfo, 0, len(sm.sessions))
	for _, session := range sm.sessions {
		result = append(result, session.ToSessionInfo())
	}

	return result
}

func generateSessionID() string {
	return uuid.New().String()[:8]
}
