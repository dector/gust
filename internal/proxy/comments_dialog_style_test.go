package proxy

import (
	"regexp"
	"strings"
	"testing"
)

func TestCommentDialogsPrecisionSegments(t *testing.T) {
	script := reloadScript(1, 8765)
	for _, fragment := range []string{
		`editor.setAttribute("role","dialog")`,
		`textarea.setAttribute("aria-label","Comment feedback")`,
		`reply.setAttribute("aria-label","Reply to comment")`,
		`#__gust_comment_editor .__gust_editor_target:before{content:"Selected element"`,
		`#__gust_comment_editor button[data-save-draft]{margin-right:6px;background:#292c30;color:#e8eaed}`,
		`#__gust_comment_popover .__gust_popover_head{display:flex;align-items:center;gap:8px;padding:8px 12px;border-bottom:1px solid #484c52;order:0}`,
		`#__gust_comment_popover .__gust_popover_meta{order:1;`,
		`#__gust_comment_popover .__gust_popover_text{order:2;`,
		`#__gust_comment_popover .__gust_popover_replies{order:3;`,
		`#__gust_comment_popover .__gust_popover_replybox{order:4;`,
		`#__gust_comment_popover .__gust_popover_actions{order:5;`,
		`max-height:240px;overflow:auto`,
		`#__gust_comment_popover[hidden]{display:none}`,
		`#__gust_batch_send[hidden],#__gust_comment_editor [hidden]{display:none!important}`,
	} {
		if !strings.Contains(script, fragment) {
			t.Errorf("missing compact dialog structure/style: %s", fragment)
		}
	}
	// Inspect every declaration, including the overlay sheet loaded after the
	// editor sheet. A late legacy rule must not restore the warm theme.
	rules := regexp.MustCompile(`([^{}'"\n]+)\{([^{}]*)\}`).FindAllStringSubmatch(script, -1)
	for _, rule := range rules {
		selector, declarations := rule[1], rule[2]
		if strings.Contains(selector, "#__gust_comment_editor.__gust_batch_editor") && strings.Contains(declarations, "background:") {
			t.Errorf("batch must accent the border, not replace neutral surfaces: %s", rule[0])
		}
		if !strings.Contains(selector, "#__gust_comment_editor") && !strings.Contains(selector, "#__gust_comment_popover") {
			continue
		}
		for _, legacy := range []string{"#f9bb71", "#704423", "#49301c", "#d9cbb7", "border-radius:8px", "border-radius:12px"} {
			if strings.Contains(declarations, legacy) {
				t.Errorf("legacy dialog styling remains: %s", rule[0])
			}
		}
	}
}
