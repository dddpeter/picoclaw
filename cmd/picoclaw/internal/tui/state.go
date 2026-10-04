// Package tui implements the picoclaw terminal UI client (docs/design/
// tui-client-design.zh.md). It connects to a running gateway over the Pico
// Protocol (/pico/ws) and renders turns as a terminal timeline.
package tui

import (
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/picoclient"
)

// ItemKind classifies one timeline entry.
type ItemKind int

const (
	ItemUser ItemKind = iota
	ItemThought
	ItemToolCalls
	ItemToolFeedback
	ItemAnswer
	ItemError
	ItemAction // dim synthesized-command line (⏹ stop request, ⇄ model, …)
	ItemMedia
)

// Item is one timeline entry. Server messages are keyed by MessageID so
// streaming updates land on the same item; user/action items get local ids.
type Item struct {
	Kind      ItemKind
	ID        string
	Content   string
	ModelName string

	ToolName string
	ToolArgs string

	Streaming bool // thought/answer still receiving updates
	Expanded  bool // thought fold state
	Steering  bool // user message sent while a turn was active

	Code       string // ItemError
	ErrMessage string // ItemError

	Attachments []picoclient.Attachment
	Timestamp   time.Time
}

// State is the terminal-independent turn/timeline state machine. Apply folds
// decoded pico events into Items; the view layer renders whatever is here.
type State struct {
	Items []Item

	Generating      bool
	GeneratingSince time.Time
	Progress        string // latest progress_note (not a timeline item)
	ProgressAt      time.Time

	ModelName string
	CtxUsage  *picoclient.ContextUsage
	Usage     *picoclient.TurnUsage

	nextLocalID int
}

// NewState returns an empty state.
func NewState() *State { return &State{} }

// AddUser appends a user message, marking it steering when a turn is active.
func (s *State) AddUser(content string) {
	s.Items = append(s.Items, Item{
		Kind:      ItemUser,
		ID:        s.localID(),
		Content:   content,
		Steering:  s.Generating,
		Timestamp: time.Now(),
	})
}

// AddAction appends a dim synthesized-command line (":stop" → ⏹ …).
func (s *State) AddAction(text string) {
	s.Items = append(s.Items, Item{
		Kind:      ItemAction,
		ID:        s.localID(),
		Content:   text,
		Timestamp: time.Now(),
	})
}

// AddLocalError appends a client-side failure (send error, not server error).
func (s *State) AddLocalError(message string) {
	s.Items = append(s.Items, Item{
		Kind:       ItemError,
		ID:         s.localID(),
		ErrMessage: message,
		Timestamp:  time.Now(),
	})
}

// ToggleLastThought flips the fold of the most recent thought item and
// reports whether anything changed.
func (s *State) ToggleLastThought() bool {
	for i := len(s.Items) - 1; i >= 0; i-- {
		if s.Items[i].Kind == ItemThought {
			s.Items[i].Expanded = !s.Items[i].Expanded
			return true
		}
	}
	return false
}

// Apply folds one decoded event; it returns true when the timeline changed.
func (s *State) Apply(ev picoclient.Event) bool {
	switch ev.Type {
	case "typing.start":
		s.Generating = true
		s.GeneratingSince = time.Now()
		s.Progress = ""
		return true

	case "typing.stop":
		s.Generating = false
		s.Progress = ""
		s.endStreaming()
		return true

	case "message.create", "message.update":
		return s.applyMessage(ev)

	case "message.delete":
		for i, it := range s.Items {
			if it.ID == ev.MessageID {
				s.Items = append(s.Items[:i], s.Items[i+1:]...)
				return true
			}
		}
		return false

	case "error":
		if ev.RequestID != "" {
			for i, it := range s.Items {
				if it.Kind == ItemUser && it.ID == ev.RequestID {
					s.Items = append(s.Items[:i], s.Items[i+1:]...)
					break
				}
			}
		}
		s.Items = append(s.Items, Item{
			Kind:       ItemError,
			ID:         s.localID(),
			Code:       ev.Code,
			ErrMessage: ev.ErrMessage,
			Timestamp:  time.Now(),
		})
		s.Generating = false
		s.Progress = ""
		s.endStreaming()
		return true

	default:
		return false
	}
}

func (s *State) applyMessage(ev picoclient.Event) bool {
	if ev.IsProgressNote() {
		s.Progress = ev.Content
		s.ProgressAt = time.Now()
		return true // status line only; never a timeline item
	}
	if ev.Placeholder {
		return false
	}
	if ev.ModelName != "" {
		s.ModelName = ev.ModelName
	}
	if ev.ContextUsage != nil {
		s.CtxUsage = ev.ContextUsage
	}
	if ev.Usage != nil {
		s.Usage = ev.Usage
	}

	switch {
	case ev.IsThought():
		it := s.upsert(ev.MessageID, ItemThought)
		it.Content = ev.Content
		it.Streaming = true
		return true

	case ev.IsToolCalls():
		changed := false
		s.closeThought()
		if len(ev.ToolCalls) == 0 {
			// Server sends tool_calls kind with a narrated content but empty
			// tool list — fall back to a plain archived line.
			it := s.upsert(ev.MessageID, ItemAction)
			it.Content = strings.TrimSpace(ev.Content)
			return true
		}
		for _, tc := range ev.ToolCalls {
			if existing := s.findByID(ev.MessageID + ":" + tc.ID); existing != nil &&
				existing.Kind == ItemToolCalls {
				existing.ToolArgs = tc.Arguments
				changed = true
				continue
			}
			s.Items = append(s.Items, Item{
				Kind:      ItemToolCalls,
				ID:        ev.MessageID + ":" + tc.ID,
				Content:   ev.Content,
				ToolName:  tc.Name,
				ToolArgs:  tc.Arguments,
				Timestamp: time.Now(),
			})
			changed = true
		}
		return changed

	case ev.IsToolFeedback():
		it := s.upsert(ev.MessageID, ItemToolFeedback)
		it.Content = ev.Content
		return true

	case ev.IsAnswer():
		s.closeThought()
		it := s.upsert(ev.MessageID, ItemAnswer)
		if it.Content != ev.Content || !it.Streaming {
			it.Content = ev.Content
		}
		it.Streaming = true
		it.ModelName = ev.ModelName
		return true

	case ev.Type == "media.create" || len(ev.Attachments) > 0:
		it := s.upsert(ev.MessageID, ItemMedia)
		it.Attachments = ev.Attachments
		it.Content = ev.Content
		return true

	default:
		return false
	}
}

// upsert finds the timeline item with the given server message id (creating
// it with the given kind when absent) and returns a pointer into Items.
func (s *State) upsert(id string, kind ItemKind) *Item {
	if id != "" {
		if it := s.findByID(id); it != nil {
			return it
		}
	}
	s.Items = append(s.Items, Item{
		Kind:      kind,
		ID:        id,
		Timestamp: time.Now(),
	})
	return &s.Items[len(s.Items)-1]
}

func (s *State) findByID(id string) *Item {
	if id == "" {
		return nil
	}
	for i := range s.Items {
		if s.Items[i].ID == id {
			return &s.Items[i]
		}
	}
	return nil
}

func (s *State) closeThought() {
	for i := range s.Items {
		if s.Items[i].Kind == ItemThought {
			s.Items[i].Streaming = false
		}
	}
}

func (s *State) endStreaming() {
	for i := range s.Items {
		s.Items[i].Streaming = false
	}
}

func (s *State) localID() string {
	s.nextLocalID++
	return "local-" + strconv.Itoa(s.nextLocalID)
}
