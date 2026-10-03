package proxy

import (
	"regexp"
	"strings"
	"testing"
)

func TestOverlayGraphiteSurfaces(t *testing.T) {
	script := reloadScript(1, 8765, true, true)
	for _, id := range []string{"__gust_panel", "__gust_comments", "__gust_comment_popover"} {
		rules := regexp.MustCompile(`(?:^|[}"\n])#`+id+`\{([^}]*)\}`).FindAllStringSubmatch(script, -1)
		if len(rules) != 1 {
			t.Fatalf("%s must have one surface definition, got %d", id, len(rules))
		}
		for _, want := range []string{"background:#222427", "color:#e8eaed", "border:1px solid #484c52", "border-radius:2px", "box-shadow:0 6px 18px #0006", "box-sizing:border-box"} {
			if !strings.Contains(rules[0][1], want) {
				t.Errorf("%s surface missing %q", id, want)
			}
		}
	}
	for _, want := range []string{
		`class="__gust_panel_header"><strong>Gust</strong><span>Development</span>`,
		`.__gust_panel_header{display:flex;align-items:baseline;gap:8px;`,
		`.__gust_row{display:flex;justify-content:space-between;gap:16px;padding:4px 0;border-bottom:1px solid #484c5255}`,
		`.__gust_batch_group_title{display:flex;align-items:center;gap:6px;padding:6px 8px;background:#292c30;border-bottom:1px solid #484c52;color:#b9bec6;`,
		`.__gust_batch_group_threads{display:grid;gap:0}`,
		`input[type=checkbox]:checked{background:#626870;border-color:#aeb5bf}`,
		`gustPanel.setAttribute("aria-label", "Gust development status")`,
		`el.setAttribute("role", "alert")`,
		`background:#222427;color:#e8eaed;border-bottom:1px solid #ef4444;border-left:3px solid #ef4444;`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("overlay missing %q", want)
		}
	}
	for _, old := range []string{"backdrop-filter:blur", "#49301c", "#f9bb7155", "#d9cbb7", "text-shadow:", "[data-editor] textarea{"} {
		if strings.Contains(script, old) {
			t.Errorf("overlay retains legacy or unscoped style %q", old)
		}
	}
}
