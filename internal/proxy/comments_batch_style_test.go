package proxy

import (
	"strings"
	"testing"
)

func TestBatchSendMountsOnlyWithCommentUI(t *testing.T) {
	script := reloadScript(1, 8765, false)
	mount := strings.Index(script, "function mountCommentBubble(){")
	ui := strings.Index(script, "function createCommentUI(){")
	button := strings.Index(script, `batchSendButton.id="__gust_batch_send"`)
	if mount < 0 || ui < mount || button < mount || button > ui {
		t.Fatal("batch Send must mount with the opted-in comment UI, not the unconditional status toolbar")
	}
}

func TestBatchStylesProtectHiddenControlsAndCtrlSelection(t *testing.T) {
	script := reloadScript(1, 8765, true)
	for _, fragment := range []string{
		`#__gust_batch_send[hidden],#__gust_comment_editor [hidden]{display:none!important}`,
		`html.__gust_selecting.__gust_ctrl.__gust_batch_mode #__gust_comment_bubble,html.__gust_selecting.__gust_ctrl.__gust_batch_mode #__gust_comment_bubble *{cursor:'+batchCursor+'!important}`,
		`#__gust_comment_editor.__gust_batch_editor [data-result],#__gust_comment_editor.__gust_batch_editor small,#__gust_comment_editor.__gust_batch_editor .__gust_editor_target{color:#bfdbfe}`,
	} {
		if !strings.Contains(script, fragment) {
			t.Errorf("batch style missing %q", fragment)
		}
	}
}
