package tui

import (
	"strings"
)

// askFlow implements the step-through ask-card state machine (design
// docs/design/2026-11-07-tui-p3-design.zh.md §1), a port of the web
// frontend's ask-flow.ts semantics: single-select auto-advances, multi
// toggles until confirmed, "other…" reveals free text, review mode edits
// any question before submit.
type askFlow struct {
	questions []askQuestionView
	answers   []askAnswer
	mode      string // "step" | "review" | "input"
	current   int    // question index in step mode
	selected  int    // option-row cursor (letters + special rows)
	inputDone bool
}

// askOther is the free-text sentinel (web ask-flow.ts OTHER).
const askOther = "__other__"

// askOptionKeys mirrors the web letter chips (A–F, 6 options max shown).
var askOptionKeys = []rune("ABCDEF")

type askQuestionView struct {
	Question    string
	Header      string
	Options     []askOptionView
	MultiSelect bool
}

type askOptionView struct {
	Label       string
	Description string
}

type askAnswer struct {
	picked []string
	other  string
}

// newAskFlow builds the flow for an ask card's questions.
func newAskFlow(questions []askQuestionView) *askFlow {
	f := &askFlow{
		questions: questions,
		answers:   make([]askAnswer, len(questions)),
		mode:      "step",
	}
	f.enterQuestion()
	return f
}

// rows returns the selectable rows of the current question in step mode:
// option rows, then (options > 0) the "other" row, then confirm/skip rows.
// Row kinds: "option", "other", "confirm", "skip". The confirm row shows
// for multi-select questions AND single-select "other" flows（其他要等自由
// 文本填完，不能自动前进，需手动确认，web autoAdvance 同语义）.
func (f *askFlow) rows() []askRow {
	if len(f.questions) == 0 || f.current >= len(f.questions) {
		return nil
	}
	q := f.questions[f.current]
	ans := f.answers[f.current]
	out := make([]askRow, 0, len(q.Options)+3)
	for i, opt := range q.Options {
		out = append(out, askRow{kind: "option", label: opt.Label, option: i})
	}
	if len(q.Options) > 0 {
		out = append(out, askRow{kind: "other", label: "其他…"})
	}
	if q.MultiSelect || containsPick(ans.picked, askOther) {
		out = append(out, askRow{kind: "confirm", label: "✓ 确认本题"})
	}
	out = append(out, askRow{kind: "skip", label: "你决定"})
	return out
}

// askRow is one selectable row of the current question.
type askRow struct {
	kind  string
	label string
	// option index when kind == "option"; -1 otherwise
	option int
}

// cursorRow returns the row under the cursor (nil when out of range).
func (f *askFlow) cursorRow() *askRow {
	rows := f.rows()
	if f.selected < 0 || f.selected >= len(rows) {
		return nil
	}
	return &rows[f.selected]
}

// clampSelected keeps the cursor inside the current question's rows.
func (f *askFlow) clampSelected() {
	if f.selected < 0 {
		f.selected = 0
	}
	if n := len(f.rows()); f.selected >= n {
		f.selected = max(n-1, 0)
	}
}

// move shifts the cursor by delta within the current rows (wrapping).
func (f *askFlow) move(delta int) {
	n := len(f.rows())
	if n == 0 {
		return
	}
	f.selected = (f.selected + delta + n) % n
}

// letterRow maps a letter key (A–F) to its option row, when the current
// question has options. Returns the row index or -1.
func (f *askFlow) letterRow(key rune) int {
	if key < 'A' || key > 'Z' {
		return -1
	}
	idx := int(key - 'A')
	rows := f.rows()
	for i, r := range rows {
		if r.kind == "option" && r.option == idx {
			return i
		}
	}
	return -1
}

// activate executes the cursor row (Enter/Space). It returns an action:
// "", "advance", "back", "review", "skip", "submit-blocked".
func (f *askFlow) activate() string {
	row := f.cursorRow()
	if row == nil {
		return ""
	}
	q := f.questions[f.current]
	ans := &f.answers[f.current]
	switch row.kind {
	case "option":
		if q.MultiSelect {
			ans.picked = togglePick(ans.picked, q.Options[row.option].Label)
			return ""
		}
		// single select: pick (replacing anything else, incl. other) and advance
		ans.picked = []string{q.Options[row.option].Label}
		ans.other = ""
		f.clampSelected()
		return f.autoAdvance()
	case "other":
		// 进入自由文本输入；askOther 的选中与否由 confirmInput 按文本决定
		f.mode = "input"
		return ""
	case "confirm":
		return f.autoAdvance()
	case "skip":
		return "skip"
	}
	return ""
}

// autoAdvance mirrors web autoAdvance: single-select non-other advances,
// last question enters review.
func (f *askFlow) autoAdvance() string {
	if f.current >= len(f.questions)-1 {
		f.mode = "review"
		f.selected = 0
		return "review"
	}
	f.current++
	f.enterQuestion()
	return "advance"
}

// togglePick adds/removes a label from the picked set (order-preserving).
func togglePick(picked []string, label string) []string {
	for i, p := range picked {
		if p == label {
			return append(picked[:i:i], picked[i+1:]...)
		}
	}
	return append(picked, label)
}

// back returns Esc semantics: input mode returns to the question (keeping
// the typed buffer); first question closes (returns "close"); otherwise
// steps back one question (returns "back").
func (f *askFlow) back() string {
	if f.mode == "input" {
		f.mode = "step"
		f.clampSelected()
		return ""
	}
	if f.mode == "review" {
		f.mode = "step"
		f.current = len(f.questions) - 1
		f.clampSelected()
		return "back"
	}
	if f.current == 0 {
		return "close"
	}
	f.current--
	f.enterQuestion()
	return "back"
}

// enterQuestion lands on a question: option-less questions go straight to
// free-text input; otherwise reset the cursor onto the rows.
func (f *askFlow) enterQuestion() {
	if f.current < len(f.questions) && len(f.questions[f.current].Options) == 0 {
		f.mode = "input"
		return
	}
	if f.mode == "input" {
		f.mode = "step"
	}
	f.selected = 0
	f.clampSelected()
}

// confirmInput closes the free-text input row: non-empty text selects the
// "other" sentinel, empty text drops it（只点其他不填=未答，web 评审修订）。
// 无选项题确认后自动前进（本题没有别的可交互行）。
func (f *askFlow) confirmInput() string {
	if f.mode != "input" {
		return ""
	}
	f.mode = "step"
	if f.current >= len(f.questions) {
		return ""
	}
	q := &f.questions[f.current]
	ans := &f.answers[f.current]
	if strings.TrimSpace(ans.other) != "" {
		if !containsPick(ans.picked, askOther) {
			ans.picked = append(ans.picked, askOther)
		}
	} else {
		ans.picked = removePick(ans.picked, askOther)
	}
	if len(q.Options) == 0 {
		return f.autoAdvance()
	}
	f.clampSelected()
	return ""
}

func removePick(picked []string, label string) []string {
	for i, p := range picked {
		if p == label {
			return append(picked[:i:i], picked[i+1:]...)
		}
	}
	return picked
}

// reviewRows returns the review-mode rows: one per question plus submit.
type askReviewRow struct {
	question int // -1 for the submit row
}

func (f *askFlow) reviewRows() []askReviewRow {
	out := make([]askReviewRow, 0, len(f.questions)+1)
	for i := range f.questions {
		out = append(out, askReviewRow{question: i})
	}
	out = append(out, askReviewRow{question: -1})
	return out
}

// reviewActivate executes the cursor row in review mode: a question row
// steps back into editing it; the submit row submits when all answered
// (returns "submit" or "submit-blocked").
func (f *askFlow) reviewActivate() string {
	rows := f.reviewRows()
	if f.selected < 0 || f.selected >= len(rows) {
		return ""
	}
	row := rows[f.selected]
	if row.question >= 0 {
		f.mode = "step"
		f.current = row.question
		f.enterQuestion()
		return "back"
	}
	if _, ok := f.buildSubmitBody(); !ok {
		return "submit-blocked"
	}
	return "submit"
}

// reviewMove shifts the review cursor.
func (f *askFlow) reviewMove(delta int) {
	n := len(f.reviewRows())
	if n == 0 {
		return
	}
	f.selected = (f.selected + delta + n) % n
}

// answerText resolves one question's answer to display/submit text, or ""
// when unanswered（只点"其他"未填文本 = 未答，web 评审修订同语义）.
func (f *askFlow) answerText(i int) string {
	if i < 0 || i >= len(f.questions) || i >= len(f.answers) {
		return ""
	}
	q := &f.questions[i]
	ans := &f.answers[i]
	labels := make([]string, 0, len(ans.picked)+1)
	wantsOther := false
	for _, p := range ans.picked {
		if p == askOther {
			wantsOther = true
			continue
		}
		labels = append(labels, p)
	}
	other := strings.TrimSpace(ans.other)
	if wantsOther || len(q.Options) == 0 {
		if other != "" {
			labels = append(labels, other)
		}
	}
	return strings.Join(labels, "; ")
}

// flattenLine folds embedded newlines（web 评审 P1：多题卡的行内换行会让
// 后续答案错位，压平为"；"）.
func askFlattenLine(text string) string {
	parts := strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' })
	return strings.Join(parts, "；")
}

// buildSubmitBody assembles the reply text: one line per question (multi),
// or the whole text as the answer (single). ok=false while any question is
// unanswered (the caller must not send).
func (f *askFlow) buildSubmitBody() (string, bool) {
	resolved := make([]string, len(f.questions))
	for i := range f.questions {
		resolved[i] = f.answerText(i)
		if resolved[i] == "" {
			return "", false
		}
	}
	if len(f.questions) > 1 {
		for i := range resolved {
			resolved[i] = askFlattenLine(resolved[i])
		}
	}
	return strings.Join(resolved, "\n"), true
}

// inputRowLabel renders the current free-text input line.
func (f *askFlow) inputLabel() string {
	if f.mode != "input" || f.current >= len(f.answers) {
		return ""
	}
	return f.answers[f.current].other
}

// setInput replaces the free-text buffer for the current question.
func (f *askFlow) setInput(text string) {
	if f.current < len(f.answers) {
		f.answers[f.current].other = text
	}
}

// askDelegateMessage 是"你决定"委托文案（与 web i18n zh.json 锁定一致；
// 改词两端同步，docs/design/2026-11-07-tui-p3-design.zh.md §1.1）。
const askDelegateMessage = "你决定吧——按最优默认方案继续，并告诉我你依据的假设"
