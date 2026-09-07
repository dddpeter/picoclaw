package feishu

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

// waitForPendingApproval polls the approvals map until an entry appears
// (RequestApproval registers it right after card delivery).
func waitForPendingApproval(t *testing.T, c *FeishuChannel) *feishuApprovalPending {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		found := (*feishuApprovalPending)(nil)
		c.approvals.Range(func(_, v any) bool {
			if p, ok := v.(*feishuApprovalPending); ok {
				found = p
			}
			return false
		})
		if found != nil {
			return found
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no pending approval registered within 2s")
	return nil
}

func approvalClickEvent(cmd, id string) *callback.CardActionTriggerEvent {
	return &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_approver"},
			Action: &callback.CallBackAction{
				Value: map[string]interface{}{"cmd": cmd, "id": id},
			},
		},
	}
}

func TestRequestApprovalApprovedViaButton(t *testing.T) {
	ch, _ := newCardActionTestChannel()
	type result struct {
		approved bool
		reason   string
	}
	done := make(chan result, 1)
	go func() {
		approved, reason := ch.RequestApproval(context.Background(), "chat-appr", "sudo systemctl restart nginx")
		done <- result{approved, reason}
	}()

	pending := waitForPendingApproval(t, ch)
	resp, err := ch.handleCardAction(context.Background(), approvalClickEvent(feishuApproveCmd, pending.id))
	if err != nil {
		t.Fatalf("handleCardAction: %v", err)
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("expected success toast, got %+v", resp)
	}

	select {
	case r := <-done:
		if !r.approved || r.reason != "" {
			t.Fatalf("expected approval, got approved=%v reason=%q", r.approved, r.reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RequestApproval did not return after approval click")
	}

	// A second click on the same card must not resurrect the decision.
	if n := countApprovals(ch); n != 0 {
		t.Fatalf("pending approval must be cleaned up after resolution, have %d", n)
	}
	resp, _ = ch.handleCardAction(context.Background(), approvalClickEvent(feishuApproveCmd, pending.id))
	if resp == nil || resp.Toast == nil || strings.Contains(resp.Toast.Content, "已批准，命令将继续执行") {
		t.Fatalf("second click must resolve to already-handled, got %+v", resp)
	}
}

func TestRequestApprovalDeniedViaButton(t *testing.T) {
	ch, _ := newCardActionTestChannel()
	done := make(chan bool, 1)
	reasons := make(chan string, 1)
	go func() {
		approved, reason := ch.RequestApproval(context.Background(), "chat-appr", "rm -rf --no-preserve-root /data")
		done <- approved
		reasons <- reason
	}()

	pending := waitForPendingApproval(t, ch)
	if _, err := ch.handleCardAction(context.Background(), approvalClickEvent(feishuDenyCmd, pending.id)); err != nil {
		t.Fatalf("handleCardAction: %v", err)
	}

	select {
	case approved := <-done:
		if approved {
			t.Fatal("expected denial")
		}
		if r := <-reasons; r != approvalDenyReason {
			t.Fatalf("denial reason = %q, want %q", r, approvalDenyReason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RequestApproval did not return after deny click")
	}
}

func TestRequestApprovalTimeoutDenies(t *testing.T) {
	ch, _ := newCardActionTestChannel()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	start := time.Now()
	approved, reason := ch.RequestApproval(ctx, "chat-appr", "sudo reboot")
	if approved {
		t.Fatal("timeout must deny")
	}
	if !strings.Contains(reason, "超时") {
		t.Fatalf("reason should mention timeout, got %q", reason)
	}
	if elapsed := time.Since(start); elapsed < 70*time.Millisecond {
		t.Fatalf("returned too early: %v", elapsed)
	}
	if n := countApprovals(ch); n != 0 {
		t.Fatalf("pending approval must be cleaned up after timeout, have %d", n)
	}
}

func TestApprovalCardCarriesButtonsWithRequestID(t *testing.T) {
	card := buildFeishuApprovalCard("sudo reboot", "apr_test1234")
	data, _ := json.Marshal(card)
	for _, want := range []string{"✅ 批准", "🛑 拒绝", "apr_test1234", "sudo reboot"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("approval card missing %q: %.300s", want, string(data))
		}
	}

	// The two buttons must share one row: a column_set with two weighted
	// columns, one button per column (schema 2.0 buttons are block-level).
	body, _ := card["body"].(map[string]any)
	elements, _ := body["elements"].([]any)
	if len(elements) != 2 {
		t.Fatalf("approval card should be markdown + column_set, got %d elements", len(elements))
	}
	columnSet, ok := elements[1].(map[string]any)
	if !ok || columnSet["tag"] != "column_set" {
		t.Fatalf("second element should be the button column_set, got %#v", elements[1])
	}
	columns, _ := columnSet["columns"].([]any)
	if len(columns) != 2 {
		t.Fatalf("column_set should have two columns, got %d", len(columns))
	}
	wantCmds := []string{feishuApproveCmd, feishuDenyCmd}
	for i, col := range columns {
		colMap, ok := col.(map[string]any)
		if !ok || colMap["tag"] != "column" || colMap["width"] != "weighted" {
			t.Fatalf("column %d malformed: %#v", i, col)
		}
		colElements, _ := colMap["elements"].([]any)
		if len(colElements) != 1 {
			t.Fatalf("column %d should hold exactly one button, got %d elements", i, len(colElements))
		}
		btn, ok := colElements[0].(map[string]any)
		if !ok || btn["tag"] != "button" {
			t.Fatalf("column %d element should be a button, got %#v", i, colElements[0])
		}
		behavior, _ := btn["behaviors"].([]any)[0].(map[string]any)
		value, _ := behavior["value"].(map[string]any)
		if value["cmd"] != wantCmds[i] || value["id"] != "apr_test1234" {
			t.Errorf("button %d callback value = %v, want cmd=%s id=apr_test1234", i, value, wantCmds[i])
		}
	}
}

func countApprovals(c *FeishuChannel) int {
	n := 0
	c.approvals.Range(func(_, _ any) bool { n++; return true })
	return n
}
