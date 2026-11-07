// Package tui implements the picoclaw terminal UI client (docs/design/
// tui-client-design.zh.md). It connects to a running gateway over the Pico
// Protocol (/pico/ws) and renders turns as a terminal timeline.
package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/picoclient"
)

// modelLineRe extracts the live model id from command replies（/status 输出
// 的 "Model: <id> (provider)" 行）。命令回复不带 model_name payload，这是
// footer 显示当前模型的唯一来源。
var modelLineRe = regexp.MustCompile(`(?m)^Model:\s*([^\s(]+)`)

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
	Expanded  bool // thought / tool-feedback fold state
	Steering  bool // user message sent while a turn was active

	Approval *picoclient.ApprovalInfo // v1 审批卡（设计 §2.3）
	Ask      *picoclient.AskInfo      // v1 问询卡
	Sealed   string                   // 封存后的单行摘要（非空 = 已封存）

	Code       string // ItemError
	ErrMessage string // ItemError

	Attachments []picoclient.Attachment
	Timestamp   time.Time

	// renderCacheKey/renderCacheVal memoize renderItem output (design §5.2);
	// content-addressed, invalidated by width/raw/flag changes upstream.
	renderCacheKey string
	renderCacheVal string
}

// State is the terminal-independent turn/timeline state machine. Apply folds
// decoded pico events into Items; the view layer renders whatever is here.
type State struct {
	Title string // session title from disk meta (user > llm > derived)

	Items []Item

	Generating      bool
	GeneratingSince time.Time
	Progress        string // latest progress_note (not a timeline item)
	ProgressAt      time.Time

	ModelName string
	CtxUsage  *picoclient.ContextUsage
	Usage     *picoclient.TurnUsage

	// HistoryStale 置位 = 断线补发丢帧（stream.catchup complete=false），
	// 时间线可能缺内容；空闲时应重读磁盘历史重建（设计 §2.1）。
	HistoryStale bool

	// RawMarkdown 关闭终帧 markdown 渲染（:raw 切换，默认 false）。
	RawMarkdown bool

	// ReadOnly 会话为非 pico 渠道：composer 禁用（会话浮层切换而来）。
	ReadOnly        bool
	ReadOnlyChannel string

	// InputHistory 是已发送输入的回溯环（↑/↓，进程内存，设计 §1）。
	InputHistory []string

	nextLocalID int
}

// NewState returns an empty state.
func NewState() *State { return &State{} }

// ApplyHistory prepends disk-loaded history items and adopts the session
// title. Live items (if the user already started chatting while the load was
// in flight) stay after the history block.
func (s *State) ApplyHistory(title string, items []Item) {
	if title != "" {
		s.Title = title
	}
	if len(items) == 0 {
		return
	}
	s.Items = append(items, s.Items...)
}

// AddActionDeduped appends an action line unless the previous item is the
// same action text (repeated Ctrl+C hints must not spam the timeline).
func (s *State) AddActionDeduped(text string) {
	if n := len(s.Items); n > 0 {
		last := &s.Items[n-1]
		if last.Kind == ItemAction && last.Content == text {
			return
		}
	}
	s.AddAction(text)
}

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

// ToggleLastToolFeedback flips the expanded state of the most recent tool
// feedback item (Ctrl+T, design §1) and reports whether anything changed.
func (s *State) ToggleLastToolFeedback() bool {
	for i := len(s.Items) - 1; i >= 0; i-- {
		if s.Items[i].Kind == ItemToolFeedback {
			s.Items[i].Expanded = !s.Items[i].Expanded
			return true
		}
	}
	return false
}

// LastAnswer returns the content of the nth-latest answer item (n=1 is the
// most recent) for :copy / Ctrl+X.
func (s *State) LastAnswer(n int) (string, bool) {
	if n < 1 {
		n = 1
	}
	for i := len(s.Items) - 1; i >= 0; i-- {
		if s.Items[i].Kind == ItemAnswer {
			n--
			if n == 0 {
				return s.Items[i].Content, true
			}
		}
	}
	return "", false
}

// PushInputHistory appends a sent input to the recall ring (deduped tail).
func (s *State) PushInputHistory(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if n := len(s.InputHistory); n > 0 && s.InputHistory[n-1] == text {
		return
	}
	s.InputHistory = append(s.InputHistory, text)
	if len(s.InputHistory) > 100 {
		s.InputHistory = s.InputHistory[len(s.InputHistory)-100:]
	}
}

// Apply folds one decoded event; it returns true when the timeline changed.
func (s *State) Apply(ev picoclient.Event) bool {
	switch ev.Type {
	case "typing.start":
		s.Generating = true
		s.GeneratingSince = time.Now()
		s.Progress = ""
		return true

	case "turn.status":
		if ev.TurnActive == nil {
			return false
		}
		if *ev.TurnActive {
			// Reconnected into a live turn: restore the generating state so
			// the UI keeps the stop affordance and spinner (design §2.2).
			if !s.Generating {
				s.Generating = true
				s.GeneratingSince = time.Now()
				return true
			}
			return false
		}
		if s.Generating {
			s.Generating = false
			s.Progress = ""
			s.endStreaming()
			return true
		}
		return false

	case "stream.catchup":
		if ev.Catchup == nil || ev.Catchup.Complete {
			return false
		}
		s.HistoryStale = true
		s.AddAction(fmt.Sprintf("⚠ 断线期间丢失 %d 帧，可能需要等待回合结束后重建历史", ev.Catchup.Dropped))
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

	// 审批/问询回执封存（设计 §2.3）：回执文本不再单独入时间线，而是把
	// 最近一张未封存的对应卡片降级为单行摘要；孤儿回执照常入时间线。
	if verdict, _ := approvalReceipt(ev.Content); verdict != "" {
		if s.sealPendingCard(false, verdict, ev) {
			return true
		}
	}
	if verdict, _ := askReceipt(ev.Content); verdict != "" {
		if s.sealPendingCard(true, verdict, ev) {
			return true
		}
	}

	if ev.ModelName != "" {
		s.ModelName = ev.ModelName
	} else if ev.Content != "" {
		if mm := modelLineRe.FindStringSubmatch(ev.Content); mm != nil {
			s.ModelName = strings.TrimSpace(mm[1])
		}
	}
	if ev.ContextUsage != nil {
		s.CtxUsage = ev.ContextUsage
	}
	if ev.Usage != nil {
		s.Usage = ev.Usage
	}

	switch {
	case ev.Approval != nil:
		it := s.upsert(ev.MessageID, ItemToolFeedback)
		it.Content = ev.Content
		it.Approval = ev.Approval
		return true

	case ev.Ask != nil:
		it := s.upsert(ev.MessageID, ItemToolFeedback)
		it.Content = ev.Content
		it.Ask = ev.Ask
		return true

	case isV0ApprovalAsk(ev.Content):
		// v0 兜底：旧网关/历史回放只有固定文本特征（跨语言契约，
		// approval_hook.go publishAsk），解析成同构卡片。
		it := s.upsert(ev.MessageID, ItemToolFeedback)
		it.Content = ev.Content
		it.Approval = parseV0Approval(ev.Content)
		return true

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

// 审批回执前缀（pkg/agent/approval_hook.go 锁定的跨语言契约，勿改词）。
const (
	approvalReceiptAlways   = "✅ 已批准；本会话内命中规则"
	approvalReceiptAllowAll = "✅ 已批准；本会话内所有审批将自动放行"
	approvalReceiptApproved = "✅ 已批准，继续执行。"
	approvalReceiptDenied   = "⛔ 已拒绝本次工具执行。"
	approvalReceiptTimeout  = "⛔ 审批超时"
	approvalReceiptNoPend   = "当前没有待批准的操作"
	approvalReceiptRevoked  = "🔓 已撤销本会话的自动放行"
)

// 问询回执前缀（pkg/agent/ask_user.go publishAskReceipt* 锁定）。
const (
	askReceiptReceived = "📝 已收到回答"
	askReceiptSkipped  = "⏭️ 已跳过问询"
	askReceiptTimeout  = "⏰ 问询超过"
	askReceiptOrphan   = "❓ 当前没有待回答的问题"
)

// approvalReceipt classifies a message content as an approval receipt.
func approvalReceipt(content string) (verdict, text string) {
	c := strings.TrimSpace(content)
	switch {
	case strings.HasPrefix(c, approvalReceiptApproved),
		strings.HasPrefix(c, approvalReceiptAlways),
		strings.HasPrefix(c, approvalReceiptAllowAll):
		return "approved", c
	case strings.HasPrefix(c, approvalReceiptDenied):
		return "denied", c
	case strings.HasPrefix(c, approvalReceiptTimeout):
		return "timeout", c
	case strings.HasPrefix(c, approvalReceiptRevoked),
		strings.HasPrefix(c, approvalReceiptNoPend):
		// Revoke/no-pending are session-level notices, not answers to a card;
		// they fall through to the normal timeline.
		return "", ""
	}
	return "", ""
}

// askReceipt classifies a message content as an ask receipt.
func askReceipt(content string) (verdict, text string) {
	c := strings.TrimSpace(content)
	switch {
	case strings.HasPrefix(c, askReceiptReceived):
		return "answered", c
	case strings.HasPrefix(c, askReceiptSkipped):
		return "skipped", c
	case strings.HasPrefix(c, askReceiptTimeout):
		return "timeout", c
	case strings.HasPrefix(c, askReceiptOrphan):
		return "", ""
	}
	return "", ""
}

// v0 审批问文本特征（approval_hook.go publishAsk 锁定文案，勿改词）。
const v0ApprovalAskPrefix = "⚠️ 需要批准：即将执行工具 "
const v0ApprovalReplyPrefix = "回复 /approve"

// isV0ApprovalAsk 报告 content 是否 v0 审批问文本。
func isV0ApprovalAsk(content string) bool {
	return strings.HasPrefix(strings.TrimSpace(content), v0ApprovalAskPrefix)
}

// parseV0Approval 解析 v0 文本：首行 = 工具名；「回复 /approve」行之前为
// preview；超时从「超过 XhYm 未回复」提取（对齐 web approval.ts）。
func parseV0Approval(content string) *picoclient.ApprovalInfo {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	tool := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[0]), v0ApprovalAskPrefix))
	if tool == "" {
		tool = "tool"
	}
	info := &picoclient.ApprovalInfo{Tool: tool}
	replyIdx := -1
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), v0ApprovalReplyPrefix) {
			replyIdx = i
			break
		}
	}
	end := len(lines)
	if replyIdx > 0 {
		end = replyIdx
		if m := v0TimeoutRe.FindStringSubmatch(lines[replyIdx]); m != nil {
			info.TimeoutMS = parseGoDurationMS(m[1])
		}
	}
	if end > 1 {
		info.Preview = strings.TrimSpace(strings.Join(lines[1:end], "\n"))
	}
	return info
}

var v0TimeoutRe = regexp.MustCompile(`超过\s*([0-9hms\.]+)\s*未回复`)

// parseGoDurationMS 解析 Go duration 片段（5m / 1h30m / 500ms，单位可混排）。
func parseGoDurationMS(s string) int64 {
	unitMs := map[byte]int64{'h': 3_600_000, 'm': 60_000, 's': 1_000}
	var total int64
	re := regexp.MustCompile(`(\d+(?:\.\d+)?)(ms|[hms])`)
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		v := parseFloat(m[1])
		if m[2] == "ms" {
			total += int64(v)
		} else {
			total += int64(v * float64(unitMs[m[2][0]]))
		}
	}
	return total
}

func parseFloat(s string) float64 {
	var f float64
	var frac bool
	var fracDiv float64 = 1
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			if frac {
				fracDiv *= 10
				f += float64(r-'0') / fracDiv
			} else {
				f = f*10 + float64(r-'0')
			}
		case r == '.':
			frac = true
		}
	}
	return f
}

// sealPendingCard seals the latest unsealed approval (ask=false) or ask
// (ask=true) card with a one-line verdict summary; the receipt message
// itself is swallowed. Reports whether a card was sealed.
func (s *State) sealPendingCard(ask bool, verdict string, ev picoclient.Event) bool {
	for i := len(s.Items) - 1; i >= 0; i-- {
		it := &s.Items[i]
		if it.Sealed != "" {
			continue
		}
		if ask && it.Ask == nil || !ask && it.Approval == nil {
			continue
		}
		it.Sealed = sealSummary(verdict, ev.Content)
		return true
	}
	return false
}

func sealSummary(verdict, content string) string {
	switch verdict {
	case "approved":
		return "✅ 已批准"
	case "denied":
		return "⛔ 已拒绝"
	case "timeout":
		return "⏰ 超时未答复"
	case "answered":
		return "📝 已回答"
	case "skipped":
		return "⏭️ 已跳过"
	}
	return firstLine(content)
}
