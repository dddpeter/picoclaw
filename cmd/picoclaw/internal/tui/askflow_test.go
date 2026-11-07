package tui

import (
	"strings"
	"testing"
)

func singleAskFlow() *askFlow {
	return newAskFlow([]askQuestionView{{
		Question: "选一个", Header: "偏好",
		Options: []askOptionView{
			{Label: "A 选项"}, {Label: "B 选项", Description: "第二个"}, {Label: "C 选项"},
		},
	}})
}

func TestAskFlow_SingleSelectAutoAdvance(t *testing.T) {
	f := singleAskFlow()
	if f.mode != "step" || f.current != 0 {
		t.Fatalf("fresh flow: %s/%d", f.mode, f.current)
	}
	// 选中第一个选项（cursor 0）→ 前进 → 唯一题进 review。
	if got := f.activate(); got != "review" {
		t.Fatalf("single question select should enter review, got %q", got)
	}
	if f.mode != "review" || f.answers[0].picked[0] != "A 选项" {
		t.Fatalf("state wrong: %s %+v", f.mode, f.answers[0])
	}
	// review 提交行在末尾；移动到它并提交。
	f.reviewMove(+1)
	if got := f.reviewActivate(); got != "submit" {
		t.Fatalf("review submit failed: %q", got)
	}
	body, ok := f.buildSubmitBody()
	if !ok || body != "A 选项" {
		t.Fatalf("submit body wrong: %q %v", body, ok)
	}
}

func TestAskFlow_SingleSelectMultiQuestionAdvance(t *testing.T) {
	f := newAskFlow([]askQuestionView{
		{Question: "一", Options: []askOptionView{{Label: "甲"}, {Label: "乙"}}},
		{Question: "二", Options: []askOptionView{{Label: "丙"}, {Label: "丁"}}},
	})
	// 选第二项（cursor 1）→ 前进到第二题。
	f.move(+1)
	if got := f.activate(); got != "advance" || f.current != 1 {
		t.Fatalf("advance wrong: %q cur=%d", got, f.current)
	}
	// 第二题选第一项 → review。
	if got := f.activate(); got != "review" {
		t.Fatalf("last question should review, got %q", got)
	}
	body, ok := f.buildSubmitBody()
	if !ok || body != "乙\n丙" {
		t.Fatalf("multi-question body wrong: %q", body)
	}
}

func TestAskFlow_MultiSelectToggleAndConfirm(t *testing.T) {
	f := newAskFlow([]askQuestionView{{
		Question: "多选", MultiSelect: true,
		Options: []askOptionView{{Label: "X"}, {Label: "Y"}, {Label: "Z"}},
	}})
	f.activate() // toggle X（多选 Enter=切换）
	f.move(+1)
	f.activate() // toggle Y
	if len(f.answers[0].picked) != 2 {
		t.Fatalf("multi toggle wrong: %+v", f.answers[0])
	}
	f.move(+1)
	f.activate() // toggle Z on → [X Y Z]
	if len(f.answers[0].picked) != 3 {
		t.Fatalf("toggle-on wrong: %+v", f.answers[0])
	}
	f.activate() // toggle Z off → [X Y]
	if len(f.answers[0].picked) != 2 {
		t.Fatalf("toggle-off wrong: %+v", f.answers[0])
	}
	// 确认行在选项后：cursor 到 confirm 并激活 → review。
	f.move(+1) // other 行
	f.move(+1) // confirm 行
	if rows := f.rows(); rows[f.selected].kind != "confirm" {
		t.Fatalf("cursor should be on confirm: %+v", rows[f.selected])
	}
	if got := f.activate(); got != "review" {
		t.Fatalf("confirm should advance: %q", got)
	}
	body, ok := f.buildSubmitBody()
	if !ok || body != "X; Y" {
		t.Fatalf("body wrong: %q", body)
	}
}

func TestAskFlow_OtherRequiresText(t *testing.T) {
	f := singleAskFlow()
	// 进入“其他”输入：cursor 到 other 行。
	f.move(+1)
	f.move(+1) // cursor 2 = C？不对：选项 3 个，other 在 index 3。
	f.selected = 3
	if rows := f.rows(); rows[3].kind != "other" {
		t.Fatalf("row 3 should be other: %+v", rows[3])
	}
	f.activate()
	if f.mode != "input" {
		t.Fatalf("activate other should enter input mode, got %s", f.mode)
	}
	// 空文本确认 → 未选其他 → 未答（web 评审修订同语义）。
	f.confirmInput()
	if _, ok := f.buildSubmitBody(); ok {
		t.Fatal("empty other must stay unanswered")
	}
	// 填文本确认 → other 生效。
	f.mode = "input"
	f.setInput("自定义答案")
	if act := f.confirmInput(); act != "advance" && act != "review" && act != "" {
		t.Fatalf("unexpected action %q", act)
	}
	body, ok := f.buildSubmitBody()
	if !ok || body != "自定义答案" {
		t.Fatalf("other text body wrong: %q %v", body, ok)
	}
}

func TestAskFlow_NoOptionQuestionGoesInput(t *testing.T) {
	f := newAskFlow([]askQuestionView{{Question: "说说你的想法"}})
	if f.mode != "input" {
		t.Fatalf("option-less question should start in input mode, got %s", f.mode)
	}
	f.setInput("自由文本\n多行也行")
	// 无选项题 Enter 确认后自动前进（唯一题 → review）。
	if got := f.confirmInput(); got != "review" {
		t.Fatalf("confirm should advance to review: %q", got)
	}
	body, ok := f.buildSubmitBody()
	// 单题：整条消息原文，不压平换行。
	if !ok || body != "自由文本\n多行也行" {
		t.Fatalf("single-question raw body wrong: %q", body)
	}
}

func TestAskFlow_MultiQuestionFlattenAndFold(t *testing.T) {
	f := newAskFlow([]askQuestionView{
		{Question: "一", Options: []askOptionView{{Label: "甲"}}},
		{Question: "二"}, // 无选项，自由文本
	})
	f.activate() // 一选甲 → 前进到二（自动 input 模式）
	if f.mode != "input" || f.current != 1 {
		t.Fatalf("should land on q2 input: %s/%d", f.mode, f.current)
	}
	f.setInput("第二题\n带换行的回答")
	f.confirmInput() // → review
	body, ok := f.buildSubmitBody()
	// 多题：内嵌换行压平为“；”，每题一行。
	if !ok || body != "甲\n第二题；带换行的回答" {
		t.Fatalf("flatten body wrong: %q", body)
	}
}

func TestAskFlow_BackAndReviewEdit(t *testing.T) {
	f := newAskFlow([]askQuestionView{
		{Question: "一", Options: []askOptionView{{Label: "甲"}, {Label: "乙"}}},
		{Question: "二", Options: []askOptionView{{Label: "丙"}}},
	})
	f.activate() // 选甲，前进到二
	if f.current != 1 {
		t.Fatal("should be on q2")
	}
	if got := f.back(); got != "back" || f.current != 0 {
		t.Fatalf("back wrong: %q cur=%d", got, f.current)
	}
	if got := f.back(); got != "close" {
		t.Fatalf("back on first question should close: %q", got)
	}
	// 重新走到 review 并回改第一题。
	f.activate() // 甲，前进
	f.activate() // 丙，review
	if f.mode != "review" {
		t.Fatal("should be in review")
	}
	f.reviewActivate() // review 行 0 = 回到第一题
	if f.mode != "step" || f.current != 0 {
		t.Fatalf("review edit wrong: %s/%d", f.mode, f.current)
	}
	f.move(+1)
	f.activate() // 改选乙 → review
	body, ok := f.buildSubmitBody()
	if !ok || body != "乙\n丙" {
		t.Fatalf("edited body wrong: %q", body)
	}
}

func TestAskFlow_LetterQuickSelect(t *testing.T) {
	f := singleAskFlow()
	if row := f.letterRow('B'); row != 1 {
		t.Fatalf("letter B should map to row 1: %d", row)
	}
	if row := f.letterRow('Z'); row != -1 {
		t.Fatalf("out-of-range letter: %d", row)
	}
	f.selected = f.letterRow('C')
	if got := f.activate(); got != "review" || f.answers[0].picked[0] != "C 选项" {
		t.Fatalf("letter select wrong: %q %+v", got, f.answers[0])
	}
}

func TestAskFlow_SkipRow(t *testing.T) {
	f := singleAskFlow()
	rows := f.rows()
	f.selected = len(rows) - 1 // skip 行始终在末尾
	if rows[f.selected].kind != "skip" {
		t.Fatalf("last row should be skip: %+v", rows[f.selected])
	}
	if got := f.activate(); got != "skip" {
		t.Fatalf("skip action wrong: %q", got)
	}
	if askDelegateMessage == "" || !strings.Contains(askDelegateMessage, "你决定") {
		t.Fatal("delegate message must match the locked web copy")
	}
}

func TestAskFlow_SubmitBlockedWhileUnanswered(t *testing.T) {
	f := newAskFlow([]askQuestionView{
		{Question: "一", Options: []askOptionView{{Label: "甲"}}},
		{Question: "二", Options: []askOptionView{{Label: "乙"}}},
	})
	f.activate() // 答一，前进
	f.activate() // 答二 → review
	// 回改第一题但不作答（Esc 返回保持未选状态不会发生——直接构造未答态）。
	f.answers[0].picked = nil
	if _, ok := f.buildSubmitBody(); ok {
		t.Fatal("unanswered question must block submit")
	}
	f.reviewRows()
	f.selected = 2 // 提交行在末尾（q0、q1 之后）
	if got := f.reviewActivate(); got != "submit-blocked" {
		t.Fatalf("expected submit-blocked, got %q", got)
	}
}
