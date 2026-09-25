package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dector/gust/internal/assets"
	"github.com/dector/gust/internal/comments"
	"github.com/dector/gust/internal/config"
	"github.com/dector/gust/internal/coordinator"
)

func TestWindOverlayInjectedWithoutComments(t *testing.T) {
	script := reloadScript(1, 8765, false)
	for _, fragment := range []string{
		`createToolbar();`,
		`if (false) { selecting=loadCommentMode(); createCommentUI();`,
		`gustPanel.appendChild(commentToolbar);`,
		`windButton.setAttribute("aria-pressed",String(windEnabled))`,
		`localStorage.setItem("__gust_wind",enabled?"1":"0")`,
		`if(!windEnabled||windMotion.matches||document.hidden)return;`,
		`immediate?0:3000+Math.random()*4000`,
		`function windTraceCount(){`,
		`return roll<.70?1:roll<.85?2:roll<.94?3:roll<.98?4:5;`,
		`for(let n=0;n<windTraceCount();n++){`,
		`const length=path.getTotalLength(), delay=Math.random()*1500;`,
		`path.style.strokeDasharray="180 "+(length+180);`,
		`path.style.setProperty("--gust-wind-end",-(length+180));`,
		`path.style.animationDelay=delay+"ms";`,
		`trace.sparks.push({offset:8+Math.random()*140`,
		`const elapsed=(now-started-tr.delay)/period;`,
		`windFrame=running?requestAnimationFrame(moveFollowers):null;`,
		`spark.style.animationDuration=(3+Math.random()*7)+"s";`,
		`animation:__gust_wind_stream 9s linear both`,
		`animation-name:__gust_wind_wander;animation-timing-function:linear;animation-fill-mode:both`,
		`clearWind();scheduleWind(enabled);`,
		`windEnabled=loadWind();windButton.setAttribute("aria-pressed",String(windEnabled));scheduleWind(true);`,
		`animation:__gust_wind_fade 11s ease-in-out both`,
		`pointer-events:none!important`,
		`#__gust_wind_button{margin-left:auto}`,
	} {
		if !strings.Contains(script, fragment) {
			t.Errorf("wind injection missing %q", fragment)
		}
	}
}

func TestSoundInjectionWithOptIn(t *testing.T) {
	script := reloadScript(1, 8765, true, false, true)
	for _, fragment := range []string{
		`const soundsOn = true;`,
		`const soundAssets = {"rain-light-loop":"/__gust/sounds/rain-light-loop.ogg?v=`,
		`const soundOnSvg=`,
		`const soundOffSvg=`,
		`soundButton=document.createElement("button")`,
		`soundButton.id="__gust_sound_button";`,
		`soundIcon.id = "__gust_sound_icon";`,
		`commentToolbar.appendChild(soundButton);`,
		`const iconRow = document.createElement("div");`,
		`iconRow.id = "__gust_icons";`,
		`iconRow.appendChild(soundIcon);`,
		`iconRow.appendChild(gustIcon);`,
		`widget.appendChild(iconRow);`,
		`#__gust_icons{display:flex;align-items:center;gap:4px}`,
		`function setSound(enabled){`,
		`if(enabled)playSound();else pauseSound();`,
		`function pauseSound(){`,
		`rainAudio.loop=true;`,
		`const soundVolume=0.8;`,
		`rainAudio.volume=soundVolume;`,
		`thunderAudio.volume=soundVolume;`,
		`track.onended=function(){`,
		`const gap=180000+Math.random()*120000;`,
		`localStorage.setItem("__gust_sound",enabled?"1":"0")`,
		`sessionStorage.setItem("__gust_sound_revealed","1")`,
		`#__gust_sound_icon{`,
		`#__gust_sound_button svg{width:20px;height:20px}`,
	} {
		if !strings.Contains(script, fragment) {
			t.Errorf("sound injection missing %q", fragment)
		}
	}
	// The icons live in their own row; the widget must stay a column so the
	// panel stacks below the icon instead of pushing it to the panel center.
	if strings.Contains(script, `#__gust_widget{display:flex`) {
		t.Error("sound injection must not override the widget column layout")
	}
	// Sound controls sit to the right of the main Gust icon.
	gustAt := strings.Index(script, `iconRow.appendChild(gustIcon);`)
	soundAt := strings.Index(script, `if (soundsOn) iconRow.appendChild(soundIcon);`)
	if gustAt < 0 || soundAt < 0 || gustAt > soundAt {
		t.Errorf("sound icon should be appended after the Gust icon (gust=%d sound=%d)", gustAt, soundAt)
	}
}

func TestSoundInjectionDisabledWithoutOptIn(t *testing.T) {
	script := reloadScript(1, 8765, true)
	if !strings.Contains(script, `const soundsOn = false;`) {
		t.Fatal("sound injection should be disabled without opt-in")
	}
	if !strings.Contains(script, `if (soundsOn) {`) {
		t.Fatal("sound UI should be guarded by the sounds opt-in")
	}
}

func TestToolboxInjected(t *testing.T) {
	for _, script := range []string{reloadScript(1, 8765), reloadScript(1, 8765, true, false, true)} {
		for _, fragment := range []string{
			`let gustToolbox;`,
			`let gustToolboxPanel;`,
			`gustToolbox = document.createElement("div");`,
			`gustToolbox.id = "__gust_toolbox";`,
			`gustToolboxPanel = document.createElement("div");`,
			`gustToolboxPanel.id = "__gust_toolbox_panel";`,
			`function mountToolbox(){`,
			`handle.id = "__gust_toolbox_handle";`,
			`function toggleToolbox(){`,
			`gustToolbox.addEventListener("click", function(e){ e.stopPropagation(); toggleToolbox(); });`,
			`/* Toolbox disabled for now.`,
			`document.body.appendChild(gustToolboxPanel);`,
			`document.body.appendChild(gustToolbox);`,
			`#__gust_toolbox{position:fixed;left:50%;bottom:0;transform:translate(-50%,11px);`,
			`#__gust_toolbox:not(.__gust_toolbox_open):hover{transform:translate(-50%,0)}`,
			`#__gust_toolbox.__gust_toolbox_open{transform:translate(-50%,calc(-1 * min(340px,42vh)))}`,
			`#__gust_toolbox_handle{display:grid;place-items:center;width:100%;height:100%;color:#7f776d;`,
			`#__gust_toolbox_panel{position:fixed;left:0;right:0;bottom:0;`,
			`transform:translateY(100%);pointer-events:none;`,
			`#__gust_toolbox_panel.__gust_toolbox_open{transform:translateY(0);pointer-events:auto}`,
			`width:132px;height:36px`,
		} {
			if !strings.Contains(script, fragment) {
				t.Errorf("toolbox injection missing %q", fragment)
			}
		}
	}
}

// TestGustBubbleHidden pins the hidden bottom bubble. The bubble is not
// removed: its mount code and its styles stay in the injected script, the
// mount code behind one comment block, so nothing can inject the bubble any
// more and uncommenting brings it straight back. It is not the way into the
// comment UI - the Gust icon, the comment mode bubble, the unread badge and the
// pins all are - so hiding it leaves every path open.
func TestGustBubbleHidden(t *testing.T) {
	for _, script := range []string{reloadScript(1, 8765), reloadScript(1, 8765, true, false, true)} {
		// The note, then one block comment from there to the disabled toolbox.
		start := strings.Index(script, "/* Bottom bubble hidden for now; kept for future features.")
		if start < 0 {
			t.Fatal("the bottom bubble must stay behind a note about future features, not be deleted")
		}
		blockEnd := strings.Index(script[start:], "*/\n/* Toolbox disabled for now.")
		if blockEnd < 0 {
			t.Fatal("the bottom bubble block must be commented out and close before the disabled toolbox")
		}
		block := script[start : start+blockEnd]
		// Everything that mounts and drives the bubble is inside that block.
		for _, kept := range []string{
			`function mountBubble(){`,
			`gustBubble.id = "__gust_bubble";`,
			`gustBubble.innerHTML = gustMarkSvg;`,
			`gustBubble.addEventListener("click", function(){ toggleToolbox(`,
			`function toggleToolbox(open){`,
			`gustBubble.classList.toggle("__gust_toolbox_open", open);`,
			`document.body.appendChild(gustBubble);`,
			`if (document.body) mountBubble();`,
		} {
			if !strings.Contains(block, kept) {
				t.Errorf("hidden bubble block must keep %q", kept)
			}
		}
		// Markup and handlers are untouched: the variable, the mark and the
		// click, the hover and the transform are all still there.
		for _, kept := range []string{
			`let gustBubble;`,
			`const gustMarkSvg='<svg`,
		} {
			if !strings.Contains(script, kept) {
				t.Errorf("hidden bubble must keep %q", kept)
			}
		}
		// So are the styles, in a live string, not in the comment.
		for _, style := range []string{
			`#__gust_bubble{position:fixed;left:50%;bottom:14px;`,
			`width:40px;height:40px`,
			`background:#ffffff0d;border:1px solid #ffffff24;border-radius:999px;backdrop-filter:blur(12px)`,
			`cursor:pointer;`,
			`#__gust_bubble:hover{transform:translate(-50%,-6px);`,
			`#__gust_bubble.__gust_toolbox_open{width:min(400px,calc(100vw - 32px));height:90px;padding:12px 14px;border-radius:1.2rem;color:#ffd9a8}`,
			`#__gust_bubble.__gust_toolbox_open svg{width:64px;height:64px}`,
		} {
			if !strings.Contains(script, style) {
				t.Errorf("hidden bubble must keep its styles %q", style)
			}
		}
		// A hidden node keeps its id in the shared list, so the guard and the
		// captured-context strip still recognise it.
		if !strings.Contains(script, `const gustNodes = "#__gust_widget,#__gust_bubble,`) {
			t.Error("#__gust_bubble must stay in the shared injected node list")
		}
		// Every mention of the mount path lives inside that one block, so no
		// code left can put the bubble in the page.
		for _, only := range []string{
			`function mountBubble(){`,
			`mountBubble, {once:true});`,
			`function toggleToolbox(open){`,
			`gustBubble.classList.toggle("__gust_toolbox_open", open);`,
			`gustBubble.id = "__gust_bubble";`,
			`document.body.appendChild(gustBubble);`,
		} {
			if !strings.Contains(block, only) {
				t.Errorf("hidden bubble block must keep %q", only)
			}
			if got, want := strings.Count(script, only), strings.Count(block, only); got != want {
				t.Errorf("%q must stay inside the hidden block: %d in the script, %d in the block", only, got, want)
			}
		}
		// The message count lives in the thread pin, not in the toolbox bubble.
		for _, gone := range []string{
			`bubbleCount`,
			`updateBubbleCount`,
			`__gust_bubble_count`,
		} {
			if strings.Contains(script, gone) {
				t.Errorf("bubble still carries a message count: %q", gone)
			}
		}
	}
}

func TestCommentModeBubbleInjected(t *testing.T) {
	script := reloadScript(1, 8765, true, true)
	for _, fragment := range []string{
		`let commentBubble = null;`,
		`function mountCommentBubble(){`,
		`commentBubble.id="__gust_comment_bubble";`,
		`commentBubble.innerHTML=commentAddIconSvg;`,
		`commentBubble.setAttribute("aria-pressed","false");`,
		`commentBubble.addEventListener("click",function(e){e.stopPropagation();beginCommentTool();});`,
		`[commentBubble]`,
		`#__gust_comment_bubble{position:fixed;right:14px;top:50%;`,
		`width:40px;height:40px`,
		`color:#f9bb71;background:#ffffff0d;border:1px solid #ffffff24`,
		`transform:translateY(-50%);transition:background .18s ease,border-color .18s ease,color .18s ease}`,
		`#__gust_comment_bubble:hover{background:#ffffff1a;border-color:#ffffff3d;color:#ffd9a8}`,
		`#__gust_comment_bubble[aria-pressed=true]{background:#f9bb71;border-color:#fff7e9;color:#24180f;`,
		`@media (prefers-reduced-motion:reduce){#__gust_comment_bubble{transition:none}}`,
		`#__gust_comment_bubble:focus-visible{outline:2px solid #f9bb71;outline-offset:2px}`,
		`html.__gust_selecting #__gust_widget,html.__gust_selecting #__gust_widget *,html.__gust_selecting #__gust_comment_bubble,`,
		`html.__gust_selecting #__gust_widget button,html.__gust_selecting #__gust_comment_bubble,`,
	} {
		if !strings.Contains(script, fragment) {
			t.Errorf("comment mode bubble injection missing %q", fragment)
		}
	}

	mountStart := strings.Index(script, `function mountCommentBubble(){`)
	if mountStart < 0 {
		t.Fatal("could not isolate comment mode bubble mounting")
	}
	mountEnd := strings.Index(script[mountStart:], `function createCommentUI(){`)
	if mountEnd < 0 {
		t.Fatal("could not isolate comment mode bubble mounting")
	}
	mount := script[mountStart : mountStart+mountEnd]
	for _, removed := range []string{
		"commentToggleButton",
		"__gust_comment_toggle",
		"const add=document.createElement(\"button\")",
		"add.addEventListener(\"click\",beginSelection)",
		"function beginSelection(",
		"commentToolbar.querySelector(\"button:not(#__gust_wind_button)\")",
	} {
		if strings.Contains(script, removed) {
			t.Errorf("legacy comment toggle %q is still present", removed)
		}
	}
	for _, unexpected := range []string{"toggleToolbox", "syncPanel", "__gust_toolbox_open", "translateY(-6px)"} {
		if strings.Contains(mount, unexpected) {
			t.Errorf("comment mode bubble must not use panel or rising behavior %q", unexpected)
		}
	}
	styleStart := strings.Index(script, `#__gust_comment_bubble{`)
	if styleStart < 0 {
		t.Fatal("could not isolate comment mode bubble styles")
	}
	styleEnd := strings.Index(script[styleStart:], `#__gust_comment_unread_badge{`)
	if styleEnd < 0 {
		t.Fatal("could not isolate comment mode bubble styles")
	}
	style := script[styleStart : styleStart+styleEnd]
	if strings.Contains(style, "transition:transform") || strings.Contains(style, ":hover{transform") {
		t.Error("comment mode bubble must not animate or rise on hover")
	}
	for _, attentionTreatment := range []string{"#dc2626", "#ef4444", "::after", "animation:"} {
		if strings.Contains(style, attentionTreatment) {
			t.Errorf("comment mode bubble must not use unread count treatment %q", attentionTreatment)
		}
	}
	activeRule := `#__gust_comment_bubble[aria-pressed=true]{background:#f9bb71;border-color:#fff7e9;color:#24180f;`
	hoverRule := `#__gust_comment_bubble:hover{background:#ffffff1a;border-color:#ffffff3d;color:#ffd9a8}`
	if !strings.Contains(style, activeRule) {
		t.Error("comment mode bubble on state must use its warm high-contrast treatment")
	}
	if !strings.Contains(style, `color:#f9bb71;background:#ffffff0d;border:1px solid #ffffff24`) {
		t.Error("comment mode bubble must retain its normal glass treatment")
	}
	if !strings.Contains(style, `@media (prefers-reduced-motion:reduce){#__gust_comment_bubble{transition:none}}`) {
		t.Error("comment mode bubble transitions must respect reduced motion")
	}
	if strings.Index(style, hoverRule) > strings.Index(style, activeRule) {
		t.Error("comment mode bubble on state must remain filled when hovered")
	}
}

func TestCommentUnreadBadgeInjected(t *testing.T) {
	script := reloadScript(1, 8765, true, true)
	for _, fragment := range []string{
		`let commentUnreadBadge = null;`,
		`function unreadPageThreads(){`,
		`c.state!=="done" && c.path===location.pathname && threadUnread(c)`,
		`function updateUnreadBadge(){`,
		`commentUnreadBadge.hidden=count===0;`,
		`function openFirstUnreadComment(){`,
		`openCommentPopover(c.id);locateComment(c);`,
		`commentUnreadBadge.id="__gust_comment_unread_badge";`,
		`commentUnreadBadge.addEventListener("click",function(e){e.preventDefault();e.stopPropagation();openFirstUnreadComment();});`,
		// The unread badge is registered in the one shared list of injected
		// Gust nodes, so it is neither a comment target nor captured context.
		`const gustNodes = "#__gust_widget,#__gust_bubble,#__gust_comment_bubble,#__gust_comment_unread_badge`,
		`+gustNodes+",details.__gust_log,#__gust_comments [data-comments]");`,
		`#__gust_comment_unread_badge{position:fixed;right:23px;top:calc(50% + 29px);`,
		`#__gust_comment_unread_badge[hidden]{display:none}`,
		`color:#fff;background:#dc2626;border:1px solid #fecaca`,
		`#__gust_comment_unread_badge:hover{background:#ef4444;border-color:#fee2e2;color:#fff}`,
		`#__gust_comment_unread_badge::after{content:"";position:absolute;inset:-3px;border:2px solid #ef4444;border-radius:999px;pointer-events:none;animation:__gust_comment_unread_pulse 1.6s ease-out infinite}`,
		`@keyframes __gust_comment_unread_pulse{0%{opacity:1;transform:scale(.9)}75%,100%{opacity:0;transform:scale(1.45)}}`,
		`@media (prefers-reduced-motion:reduce){#__gust_comment_unread_badge{transition:none}#__gust_comment_unread_badge::after{animation:none;opacity:.65;transform:scale(1)}}`,
		`#__gust_comment_unread_badge:focus-visible{outline:2px solid #f9bb71;outline-offset:2px}`,
	} {
		if !strings.Contains(script, fragment) {
			t.Errorf("comment unread badge injection missing %q", fragment)
		}
	}
	if !strings.Contains(script, `document.body.appendChild(commentBubble);document.body.appendChild(commentUnreadBadge);`) {
		t.Error("unread badge must mount as a body sibling, not inside the comment bubble")
	}
}

func TestCommentUnreadBadgeBehaviorWhenNodeAvailable(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := reloadScript(1, 8765, true, true)
	helperStart := strings.Index(script, `function threadMessages(c){`)
	helperEnd := strings.Index(script, `function popoverAuthorLabel(author){`)
	mountStart := strings.Index(script, `function mountCommentBubble(){`)
	mountEnd := strings.Index(script, `function createCommentUI(){`)
	if helperStart < 0 || helperEnd < helperStart || mountStart < 0 || mountEnd < mountStart {
		t.Fatal("could not extract comment unread badge behavior")
	}
	harness := commentUnreadBadgeHarnessPrelude + script[helperStart:helperEnd] + script[mountStart:mountEnd] + commentUnreadBadgeHarnessChecks
	file := t.TempDir() + "/comment-unread-badge.js"
	if err := os.WriteFile(file, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("comment unread badge behavior: %v\n%s", err, output)
	}
}

const commentUnreadBadgeHarnessPrelude = `class El {
  constructor() { this.attrs = {}; this.title = ''; this.hidden = false; this.innerHTML = ''; this.listeners = {}; this.children = []; }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  addEventListener(type, fn) { this.listeners[type] = fn; }
  appendChild(child) { this.children.push(child); return child; }
}
const document = { body: new El(), createElement() { return new El(); } };
const commentAddIconSvg = '<svg data-icon="add-comment"></svg>';
let commentState = [];
let commentBubble = null;
let commentUnreadBadge = null;
let popoverCommentId = null;
let commentPopover = null;
let bubbleClicks = 0;
const opened = [];
const located = [];
const location = { pathname: '/' };
const localStorage = {
  store: {},
  getItem(k) { return Object.prototype.hasOwnProperty.call(this.store, k) ? this.store[k] : null; },
  setItem(k, v) { this.store[k] = String(v); },
};
function openCommentPopover(id) { opened.push(id); popoverCommentId = id; markThreadSeen(commentState.find(function(c) { return c.id === id; })); }
function locateComment(c) { located.push(c && c.id); }
function beginCommentTool() { bubbleClicks++; }
function assert(condition, message) { if (!condition) throw new Error(message); }
`

const commentUnreadBadgeHarnessChecks = `
const old = new Date(Date.now() - 10000).toISOString();
const now = new Date().toISOString();
const agentMessage = function(text) { return { author: 'agent', text: text, createdAt: now }; };
commentState = [
  { id: 'done', path: '/', state: 'done', messages: [agentMessage('done')] },
  { id: 'other-page', path: '/other', state: 'review', messages: [agentMessage('other')] },
  { id: 'read', path: '/', state: 'review', messages: [agentMessage('read')] },
  { id: 'first', path: '/', state: 'seen', messages: [agentMessage('first')] },
  { id: 'second', path: '/', state: 'review', messages: [agentMessage('second')] },
];
localStorage.setItem('__gust_thread_seen_read', String(Date.parse(now)));
mountCommentBubble();
updateUnreadBadge();
assert(document.body.children[0] === commentBubble, 'badge test mounts the comment bubble');
assert(document.body.children[1] === commentUnreadBadge, 'badge test mounts the badge separately');
assert(commentUnreadBadge.hidden === false, 'badge is visible for unread threads');
assert(commentUnreadBadge.textContent === '2', 'badge counts only current-page unresolved unread threads');
assert(commentUnreadBadge.attrs['aria-label'] === '2 new comment threads', 'badge exposes an accessible new-thread count');
let prevented = 0;
let stopped = 0;
commentUnreadBadge.listeners.click({ preventDefault() { prevented++; }, stopPropagation() { stopped++; } });
assert(prevented === 1 && stopped === 1, 'badge click is isolated from the page');
assert(bubbleClicks === 0, 'badge click does not toggle add-comment mode');
assert(opened.length === 1 && opened[0] === 'first', 'badge opens the first unread thread');
assert(located.length === 1 && located[0] === 'first', 'badge scrolls the first unread target');
updateUnreadBadge();
assert(commentUnreadBadge.textContent === '1', 'opening a thread clears only that thread from the badge');
assert(commentUnreadBadge.hidden === false, 'badge remains visible while another thread is unread');
commentUnreadBadge.listeners.click({ preventDefault() {}, stopPropagation() {} });
assert(opened.length === 2 && opened[1] === 'second', 'a second click opens the next unread thread');
markThreadSeen(commentState[4]);
updateUnreadBadge();
assert(commentUnreadBadge.textContent === '', 'badge clears its text when no unread threads remain');
assert(commentUnreadBadge.hidden === true, 'badge hides when no unread threads remain');
console.log('comment unread badge ok');
`

func TestCommentModeBubbleBehaviorWhenNodeAvailable(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := reloadScript(1, 8765, true, true)
	modeStart := strings.Index(script, `function updateCommentUI(){`)
	modeEnd := strings.Index(script, `function cssEscape(v){`)
	mountStart := strings.Index(script, `function mountCommentBubble(){`)
	mountEnd := strings.Index(script, `function createCommentUI(){`)
	if modeStart < 0 || modeEnd < modeStart || mountStart < 0 || mountEnd < mountStart {
		t.Fatal("could not extract comment mode bubble behavior")
	}
	harness := commentModeBubbleHarnessPrelude + script[modeStart:modeEnd] + script[mountStart:mountEnd] + commentModeBubbleHarnessChecks
	file := t.TempDir() + "/comment-mode-bubble.js"
	if err := os.WriteFile(file, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("comment mode bubble behavior: %v\n%s", err, output)
	}
}

const commentModeBubbleHarnessPrelude = `class El {
  constructor() { this.attrs = {}; this.title = ''; this.hidden = false; this.innerHTML = ''; this.listeners = {}; this.children = []; }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  addEventListener(type, fn) { this.listeners[type] = fn; }
  appendChild(child) { this.children.push(child); this.child = child; return child; }
}
const document = {
  documentElement: { classList: { toggle() {} } },
  body: new El(),
  createElement() { return new El(); },
  querySelector() { return null; },
};
const commentAddIconSvg = '<svg data-icon="add-comment"></svg>';
const selfDev = false;
let commentBubble = null;
let commentUnreadBadge = null;
let badgeClicks = 0;
let selecting = false;
let editorOpen = false;
let selectedElement = null;
let selectedPoint = null;
let editorAnchor = null;
let hoverPath = [];
const modeTitle = new El();
const autoLabel = new El();
const commentToolbar = {
  querySelector(selector) {
    if (selector === '[data-mode-title]') return modeTitle;
    return null;
  },
};
const gustWidget = { classList: { toggle() {} } };
const commentUI = { querySelector(selector) { return selector === '[data-autosubmit-label]' ? autoLabel : null; } };
function setHighlight() {}
function saveCommentMode() {}
function savePinned() {}
function scheduleRenderPath() {}
function updateEditorPosition() {}
function openFirstUnreadComment() { badgeClicks++; }
let panelSyncs = 0;
function syncPanel() { panelSyncs++; }
function assert(condition, message) { if (!condition) throw new Error(message); }
`

const commentModeBubbleHarnessChecks = `
mountCommentBubble();
assert(document.body.children[0] === commentBubble, 'comment bubble mounts before the badge');
assert(document.body.children[1] === commentUnreadBadge, 'unread badge is a sibling of the bubble');
assert(commentBubble.id === '__gust_comment_bubble', 'comment bubble has a stable id');
assert(commentBubble.title === 'Add comment', 'comment bubble has a visible tooltip');
assert(commentBubble.attrs['aria-label'] === 'Add comment', 'comment bubble has an accessible name');
assert(commentBubble.attrs['aria-pressed'] === 'false', 'comment bubble starts unpressed');
assert(commentBubble.innerHTML === commentAddIconSvg, 'comment bubble uses the add-comment icon');
assert(commentUnreadBadge.id === '__gust_comment_unread_badge', 'unread badge has a stable id');
assert(commentUnreadBadge.hidden === true, 'unread badge is hidden without unread threads');
let stopped = 0;
commentBubble.listeners.click({ stopPropagation() { stopped++; } });
assert(stopped === 1, 'comment bubble click does not leak to the page');
assert(selecting === true, 'click enables comment creation mode');
assert(commentBubble.attrs['aria-pressed'] === 'true', 'active comment mode is exposed as pressed');
assert(commentBubble.attrs['aria-label'] === 'Exit comment mode', 'active comment mode has an exit label');
assert(panelSyncs === 0, 'comment bubble does not open the Gust panel');
let badgePrevented = 0;
commentUnreadBadge.listeners.click({ preventDefault() { badgePrevented++; }, stopPropagation() { stopped++; } });
assert(badgePrevented === 1, 'unread badge click does not submit or activate the page');
assert(stopped === 2, 'unread badge click does not leak to the page');
assert(badgeClicks === 1, 'unread badge delegates to the unread-thread action');
commentBubble.listeners.click({ stopPropagation() { stopped++; } });
assert(selecting === false, 'clicking again disables comment creation mode');
assert(commentBubble.attrs['aria-pressed'] === 'false', 'disabled comment mode clears the pressed state');
assert(commentBubble.attrs['aria-label'] === 'Add comment', 'disabled comment mode restores its label');
assert(panelSyncs === 0, 'toggling comment mode never opens the Gust panel');
console.log('comment mode bubble ok');
`

func TestProxyServesSoundsWithHashCache(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	proxyURL, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()
	server.SetSoundsEnabled(true)

	for _, sound := range assets.Sounds() {
		resp, err := http.Get(proxyURL + "/__gust/sounds/" + sound.Name + ".ogg")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		etag := resp.Header.Get("ETag")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d, want 200", sound.Name, resp.StatusCode)
		}
		if len(body) == 0 {
			t.Fatalf("%s served empty body", sound.Name)
		}
		if got := resp.Header.Get("Content-Type"); got != "audio/ogg" {
			t.Errorf("%s content-type=%q, want audio/ogg", sound.Name, got)
		}
		if !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
			t.Errorf("%s cache-control=%q, want immutable", sound.Name, resp.Header.Get("Cache-Control"))
		}
		if etag != `"`+sound.Hash+`"` {
			t.Errorf("%s etag=%q, want hash %q", sound.Name, etag, sound.Hash)
		}

		req, err := http.NewRequest(http.MethodGet, proxyURL+"/__gust/sounds/"+sound.Name+".ogg", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("If-None-Match", etag)
		cached, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		cached.Body.Close()
		if cached.StatusCode != http.StatusNotModified {
			t.Errorf("%s conditional status=%d, want 304", sound.Name, cached.StatusCode)
		}
	}
}

func TestProxySoundsDisabledByDefault(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	proxyURL, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()

	resp, err := http.Get(proxyURL + "/__gust/sounds/rain-light-loop.ogg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}

func TestProxyForwardsRequestsAndBodies(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/echo" || r.URL.RawQuery != "x=1" {
			t.Fatalf("unexpected app URL: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-App", "ok")
		fmt.Fprintf(w, "%s:%s", r.Method, body)
	}))
	defer app.Close()

	proxyURL, closeProxy := startProxyForTest(t, appPort(t, app.URL))
	defer closeProxy.Close()

	resp, err := http.Post(proxyURL+"/echo?x=1", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.Header.Get("X-App") != "ok" || string(body) != "POST:hello" {
		t.Fatalf("unexpected response header=%q body=%q", resp.Header.Get("X-App"), body)
	}
}

func TestProxyStreamsNonHTMLResponses(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0, 1, 2, 3})
	}))
	defer app.Close()

	proxyURL, closeProxy := startProxyForTest(t, appPort(t, app.URL))
	defer closeProxy.Close()

	resp, err := http.Get(proxyURL + "/data")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if got, want := string(body), string([]byte{0, 1, 2, 3}); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestProxyReservesGustPaths(t *testing.T) {
	appHit := false
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer app.Close()

	proxyURL, closeProxy := startProxyForTest(t, appPort(t, app.URL))
	defer closeProxy.Close()

	resp, err := http.Get(proxyURL + "/__gust/anything")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	if appHit {
		t.Fatal("reserved Gust path was forwarded to app")
	}
}

func TestProxyServesStatus(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	proxyURL, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()

	started := time.Now().Add(-time.Second).UTC()
	server.SetStatusProvider(func(context.Context) (coordinator.Status, error) {
		uptime := int64(1000)
		return coordinator.Status{Process: coordinator.ProcessStatus{
			Running: true, StartedAt: &started, UptimeMS: &uptime,
			LastExit: &coordinator.LastExit{Code: 1, At: started, PassedMS: 1000, Error: true,
				Logs: &coordinator.ProcessLogs{Stdout: "out", Stderr: "err"}},
		}}, nil
	})

	resp, err := http.Get(proxyURL + "/__gust/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("response = status %d content-type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var status coordinator.ProcessStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if !status.Running || status.UptimeMS == nil || status.LastExit == nil || status.LastExit.PassedMS != 1000 || status.LastExit.Logs.Stderr != "err" {
		t.Fatalf("status = %+v", status)
	}
}

func TestProxyServesInfo(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	proxyURL, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()

	server.SetStatusProvider(func(context.Context) (coordinator.Status, error) {
		return coordinator.Status{
			State:            coordinator.ExternalRunning,
			PID:              4242,
			Version:          7,
			AutoReloadPaused: true,
			LastTrigger:      coordinator.TriggerFS,
			LastReadyIn:      420 * time.Millisecond,
			BrowserNotice:    "before failed: templ generate (exit 1)",
		}, nil
	})

	resp, err := http.Get(proxyURL + "/__gust/info")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("response = status %d content-type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var info browserInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	want := browserInfo{Status: "running", PID: 4242, Version: 7, AutoReload: "paused", Trigger: "filesystem", ReadyMS: 420, Notice: "before failed: templ generate (exit 1)"}
	if info != want {
		t.Fatalf("info = %+v, want %+v", info, want)
	}
}

func TestProxyRetriesUntilAppAvailable(t *testing.T) {
	appPort := freePort(t)
	proxyPort := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, err := Start(ctx, config.Config{AppPort: appPort, ProxyPort: proxyPort, ProxyEnabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	go func() {
		time.Sleep(250 * time.Millisecond)
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", appPort))
		if err != nil {
			return
		}
		_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ready"))
		}))
	}()

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", proxyPort))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ready" {
		t.Fatalf("body = %q, want ready", body)
	}
}

func TestProxyCanceledRequestDoesNotReportAppUnreachable(t *testing.T) {
	started := make(chan struct{})
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			close(started)
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte("healthy"))
	}))
	defer app.Close()

	_, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()
	server.BrowserHub().BrowserReady(1)
	target, err := url.Parse(app.URL)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.handler(target)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/slow", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("slow request did not reach app")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled proxy request did not finish")
	}
	if got := server.BrowserHub().latest; got.Type != "ready" || got.Version != 1 {
		t.Fatalf("browser state after cancellation = %+v, want ready v1", got)
	}

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/health", nil))
	if resp.Code != http.StatusOK || resp.Body.String() != "healthy" {
		t.Fatalf("healthy app response = %d %q", resp.Code, resp.Body.String())
	}
}

func TestProxyUnreachableAppReportsBrowserError(t *testing.T) {
	port := freePort(t)
	_, server := startProxyForTest(t, port)
	defer server.Close()
	server.BrowserHub().BrowserReady(1)

	resp := httptest.NewRecorder()
	target, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	server.handler(target).ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/", nil))
	if resp.Code != http.StatusBadGateway {
		t.Fatalf("unreachable app response = %d, want 502", resp.Code)
	}
	if got := server.BrowserHub().latest; got.Type != "error" || got.Message != "proxy cannot reach app" {
		t.Fatalf("browser state after upstream failure = %+v, want connectivity error", got)
	}
}

func TestProxyBindFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	server, err := Start(context.Background(), config.Config{AppPort: freePort(t), ProxyPort: port, ProxyEnabled: true}, nil)
	if err == nil {
		server.Close()
		t.Fatal("expected bind failure")
	}
}

func TestProxyInjectsHTMLAndUpdatesHeaders(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept-Encoding"); got != "" {
			t.Fatalf("Accept-Encoding forwarded as %q", got)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", "12")
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
		_, _ = w.Write([]byte("<h1>hi</h1>!"))
	}))
	defer app.Close()

	proxyURL, closeProxy := startProxyForTest(t, appPort(t, app.URL))
	defer closeProxy.Close()

	req, err := http.NewRequest(http.MethodGet, proxyURL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `<script id="__gust_reload">`) {
		t.Fatalf("missing injected script in %q", body)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got, want := resp.Header.Get("Content-Length"), fmt.Sprint(len(body)); got != want {
		t.Fatalf("Content-Length = %q, want %q", got, want)
	}
	if got := resp.Header.Get("ETag"); got != "" {
		t.Fatalf("ETag = %q, want removed", got)
	}
	if got := resp.Header.Get("Last-Modified"); got != "Wed, 21 Oct 2015 07:28:00 GMT" {
		t.Fatalf("Last-Modified = %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestProxyInjectsHTMLErrorPagesAndLeavesAbsentLengthAbsent(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusInternalServerError)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("oops"))
	}))
	defer app.Close()

	proxyURL, closeProxy := startProxyForTest(t, appPort(t, app.URL))
	defer closeProxy.Close()

	resp, err := http.Get(proxyURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(string(body), "__gust_reload") {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want absent", got)
	}
}

func TestProxySkipsHTMLInjectionRules(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		reqHeader  map[string]string
		status     int
		respHeader map[string]string
		body       string
	}{
		{name: "xhtml", respHeader: map[string]string{"Content-Type": "application/xhtml+xml"}},
		{name: "head", method: http.MethodHead, respHeader: map[string]string{"Content-Type": "text/html"}},
		{name: "range request", reqHeader: map[string]string{"Range": "bytes=0-4"}, respHeader: map[string]string{"Content-Type": "text/html"}},
		{name: "partial response", status: http.StatusPartialContent, respHeader: map[string]string{"Content-Type": "text/html"}},
		{name: "attachment", respHeader: map[string]string{"Content-Type": "text/html", "Content-Disposition": "attachment; filename=x.html"}},
		{name: "encoded", respHeader: map[string]string{"Content-Type": "text/html", "Content-Encoding": "gzip"}},
		{name: "already injected", respHeader: map[string]string{"Content-Type": "text/html"}, body: `<script id="__gust_reload"></script>`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tt.respHeader {
					w.Header().Set(k, v)
				}
				status := tt.status
				if status == 0 {
					status = http.StatusOK
				}
				w.WriteHeader(status)
				body := tt.body
				if body == "" {
					body = "<html></html>"
				}
				_, _ = w.Write([]byte(body))
			}))
			defer app.Close()

			proxyURL, closeProxy := startProxyForTest(t, appPort(t, app.URL))
			defer closeProxy.Close()

			method := tt.method
			if method == "" {
				method = http.MethodGet
			}
			req, err := http.NewRequest(method, proxyURL+"/", nil)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range tt.reqHeader {
				req.Header.Set(k, v)
			}
			client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if strings.Count(string(body), "__gust_reload") > strings.Count(tt.body, "__gust_reload") {
				t.Fatalf("unexpected injection body=%q", body)
			}
			if got := resp.Header.Get("Cache-Control"); got == "no-store" && tt.name != "already injected" {
				t.Fatalf("Cache-Control set on skipped response")
			}
		})
	}
}

func TestProxyInjectsIdentityEncodedHTML(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Encoding", "identity")
		_, _ = w.Write([]byte("ok"))
	}))
	defer app.Close()

	proxyURL, closeProxy := startProxyForTest(t, appPort(t, app.URL))
	defer closeProxy.Close()

	resp, err := http.Get(proxyURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "__gust_reload") {
		t.Fatalf("body = %q, want injection", body)
	}
}

func TestProxyBrowserWebSocketSendsLatestAndReloads(t *testing.T) {
	proxyURL, closeProxy := startProxyForTest(t, freePort(t))
	defer closeProxy.Close()

	closeProxy.BrowserHub().BrowserReady(3)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(proxyURL, "http://", "ws://", 1)+"/__gust/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	readBootMessage(t, ctx, conn)
	msg := readBrowserMessage(t, ctx, conn)
	if msg.Type != "ready" || msg.Version != 3 {
		t.Fatalf("connect message = %+v, want ready v3", msg)
	}
	msg = readBrowserMessage(t, ctx, conn)
	if msg.Type != "debug" || msg.Enabled == nil || *msg.Enabled {
		t.Fatalf("connect debug message = %+v, want disabled", msg)
	}

	closeProxy.BrowserHub().BrowserReady(4)
	msg = readBrowserMessage(t, ctx, conn)
	if msg.Type != "reload" || msg.Version != 4 {
		t.Fatalf("broadcast message = %+v, want reload v4", msg)
	}

	closeProxy.BrowserHub().BrowserError("health check timed out")
	msg = readBrowserMessage(t, ctx, conn)
	if msg.Type != "error" || msg.Message != "health check timed out" {
		t.Fatalf("error message = %+v", msg)
	}
}

func TestProxyBrowserWebSocketTogglesDebug(t *testing.T) {
	proxyURL, closeProxy := startProxyForTest(t, freePort(t))
	defer closeProxy.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(proxyURL, "http://", "ws://", 1)+"/__gust/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	bootID := readBootMessage(t, ctx, conn)
	msg := readBrowserMessage(t, ctx, conn)
	if msg.Type != "debug" || msg.Enabled == nil || *msg.Enabled {
		t.Fatalf("connect message = %+v, want debug disabled", msg)
	}

	if enabled := closeProxy.BrowserHub().ToggleDebug(); !enabled {
		t.Fatal("ToggleDebug() = false, want true")
	}
	msg = readBrowserMessage(t, ctx, conn)
	if msg.Type != "debug" || msg.Enabled == nil || !*msg.Enabled {
		t.Fatalf("toggle message = %+v, want debug enabled", msg)
	}

	conn2, _, err := websocket.Dial(ctx, strings.Replace(proxyURL, "http://", "ws://", 1)+"/__gust/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close(websocket.StatusNormalClosure, "done")
	if got := readBootMessage(t, ctx, conn2); got != bootID {
		t.Fatalf("boot ID changed on reconnect: %q to %q", bootID, got)
	}
	msg = readBrowserMessage(t, ctx, conn2)
	if msg.Type != "debug" || msg.Enabled == nil || !*msg.Enabled {
		t.Fatalf("new client message = %+v, want debug enabled", msg)
	}
}

func TestProxyBrowserWebSocketSendsLatestErrorOnConnect(t *testing.T) {
	proxyURL, closeProxy := startProxyForTest(t, freePort(t))
	defer closeProxy.Close()

	closeProxy.BrowserHub().BrowserError("process exited")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(proxyURL, "http://", "ws://", 1)+"/__gust/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	readBootMessage(t, ctx, conn)
	msg := readBrowserMessage(t, ctx, conn)
	if msg.Type != "error" || msg.Message != "process exited" {
		t.Fatalf("connect message = %+v, want latest error", msg)
	}
}

func TestProxyBrowserWebSocketSendsLatestNoticeOnConnect(t *testing.T) {
	proxyURL, closeProxy := startProxyForTest(t, freePort(t))
	defer closeProxy.Close()

	closeProxy.BrowserHub().BrowserNotice("before failed: templ generate (exit 1)")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(proxyURL, "http://", "ws://", 1)+"/__gust/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	readBootMessage(t, ctx, conn)
	msg := readBrowserMessage(t, ctx, conn)
	if msg.Type != "notice" || msg.Message != "before failed: templ generate (exit 1)" {
		t.Fatalf("connect message = %+v, want latest notice", msg)
	}
}

func TestProxyInjectedScriptSyntaxWhenNodeAvailable(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	for _, script := range []string{reloadScript(12, 8765), reloadScript(12, 8765, true, true)} {
		start := strings.Index(script, ">") + 1
		end := strings.LastIndex(script, "</script>")
		if start <= 0 || end < start {
			t.Fatal("could not extract injected JavaScript")
		}
		file := t.TempDir() + "/reload.js"
		if err := os.WriteFile(file, []byte(script[start:end]), 0600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command(node, "--check", file).CombinedOutput(); err != nil {
			t.Fatalf("generated JavaScript syntax: %v\n%s", err, output)
		}
	}
}

func TestProxySelfDevScript(t *testing.T) {
	defaultScript := reloadScript(1, 8080, true)
	selfScript := reloadScript(1, 8080, true, true)
	if !strings.Contains(defaultScript, "const selfDev = false;") || !strings.Contains(selfScript, "const selfDev = true;") {
		t.Fatal("self-dev must be opt-in")
	}
	for _, expected := range []string{
		// One shared list of injected Gust nodes, used by both the comment
		// target guard and the captured-context strip. Keep these in step.
		`const gustNodes = "#__gust_widget,#__gust_bubble,#__gust_comment_bubble,#__gust_comment_unread_badge,#__gust_toolbox,#__gust_toolbox_panel,#__gust_error,[data-gust-overlay]";`,
		`function isGustNode(el){ return !!(el && el.closest && el.closest(gustNodes)); }`,
		`function isGustNodeOrPanel(el){ return isGustNode(el) || isSelfDevPanel(el) || isPrivatePanelNode(el); }`,
		`function blockedCommentTarget(el, ctrlKey){ return !ctrlKey && isGustNodeOrPanel(el); }`,
		`clone.querySelectorAll(strip)`,
		`+gustNodes+",details.__gust_log,#__gust_comments [data-comments]");`,
		`function isSelfDevPanel(el)`,
		`function isPrivatePanelNode(el)`,
		`(!gust&&isPrivatePanelNode(el)))return "";`,
		`function isGustSelection(el){ return gustSelection && isGustNodeOrPanel(el); }`,
		`if(!selecting||blockedCommentTarget(e.target,e.ctrlKey))return;`,
		`if(blockedCommentTarget(e.target,e.ctrlKey)){hoverPath=[];setHighlight(null);updateCommentUI();return;}`,
		`if(selfDev&&restartPending){restartPending=false;location.reload();}`,
		`if(bootID&&bootID!==msg.bootId)restartPending=true;`,
	} {
		if !strings.Contains(selfScript, expected) {
			t.Errorf("self-dev script missing %q", expected)
		}
	}
}

// TestCommentTargetGuardBlocksGustBubbleWhenNodeAvailable runs the real comment
// mode guard against a small DOM. It pins the deliberate parts - Ctrl selects
// any element, including every part of Gust's own UI, and a plain click still
// reaches the node's own handler instead of the comment editor. The bubble is
// hidden now, but its id stays in the shared node list, so a Gust node stays
// blocked whether or not Gust renders it today.
func TestCommentTargetGuardBlocksGustBubbleWhenNodeAvailable(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := reloadScript(1, 8765, true, true)
	nodesStart := strings.Index(script, `const gustNodes = "`)
	nodesEnd := strings.Index(script, `function setHighlight(el){`)
	moveStart := strings.Index(script, "document.addEventListener(\"mousemove\",function(e){\n  if(!selecting)return;")
	clickStart := strings.Index(script, "document.addEventListener(\"click\",function(e){\n  if(!selecting||blockedCommentTarget(e.target,")
	if nodesStart < 0 || nodesEnd < nodesStart || moveStart < 0 || clickStart < 0 {
		t.Fatal("could not extract the comment target guard")
	}
	handlers := ""
	for _, start := range []int{moveStart, clickStart} {
		end := strings.Index(script[start:], "},true);")
		if end < 0 {
			t.Fatal("could not find the end of a comment mode guard")
		}
		handlers += script[start:start+end+len("},true);")] + "\n"
	}
	harness := commentGuardHarnessPrelude + script[nodesStart:nodesEnd] + handlers + commentGuardHarnessChecks
	file := t.TempDir() + "/comment-guard.js"
	if err := os.WriteFile(file, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("comment target guard: %v\n%s", err, output)
	}
}

// TestCtrlCommentCapturesContextAndPinSurvives runs the real capture path -
// locatorFor(), safeOuterHTML() and matchingElement() - against a DOM shaped
// like Gust's own panel. A Ctrl comment on a thread row, a pin, the log or the
// count badge must carry real text and real HTML, must stay scrubbed of
// passwords, and must keep resolving a pin after the element's own text
// changes, because a thread row flips Draft -> Submitted -> In progress and the
// panel list is rebuilt on every poll. Without Ctrl nothing changes.
func TestCtrlCommentCapturesContextAndPinSurvives(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := reloadScript(1, 8765, true, true)
	spans := []struct {
		from, to string
	}{
		{`const gustNodes = "`, `function meaningfulPath(`},
		{`function cssEscape(`, `function mountCommentBubble(){`},
		{`function parseLocator(c){`, `function ensurePinOverlay(){`},
	}
	captured := ""
	for _, span := range spans {
		from, to := strings.Index(script, span.from), strings.Index(script, span.to)
		if from < 0 || to < from {
			t.Fatalf("could not extract the capture path around %q", span.from)
		}
		captured += script[from:to] + "\n"
	}
	file := t.TempDir() + "/comment-capture.js"
	if err := os.WriteFile(file, []byte(commentGuardHarnessPrelude+captured+commentCaptureHarnessChecks), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("comment capture: %v\n%s", err, output)
	}
}

const commentCaptureHarnessChecks = `
/* The real nesting: the panel and the comment list hang off #__gust_widget, the
   pin overlay off <html>, and Gust's own nodes carry [data-gust-overlay]. */
const hero = append(document.body, el('h1'));
append(hero, el('span')).text = 'Hello';
const widget = append(document.body, el('div', '__gust_widget'));
const panel = append(widget, el('div', '__gust_panel'));
const log = append(panel, el('details', '', { 'data-name': 'Server' }));
addClass(log, '__gust_log');
append(log, el('summary')).text = 'Server';
text(append(log, el('pre')), 'listening on 127.0.0.1:8080');
append(log, el('input', '', { 'type': 'password', 'value': 'hunter2' }));
const comments = append(widget, el('div', '__gust_comments'));
const header = append(comments, el('div'));
append(header, el('span')).text = 'Comments';
const count = text(append(header, el('span', '', { 'data-comment-count': '' })), '1');
const list = append(comments, el('div', '', { 'data-comments': '' }));
const overlay = append(document.documentElement, el('div', '__gust_pin_overlay'));
overlay.setAttribute('data-gust-overlay', '');

/* renderCommentState() builds a thread row, and the pin for that thread. */
function renderThread(state) {
  list.replaceChildren();
  const item = append(list, el('div', '', { 'data-comment-id': 'c1' }));
  const row = append(item, el('button'));
  addClass(row, '__gust_comment_row');
  text(append(row, el('span', '__gust_comment_text')), 'Make the badge bigger');
  const badge = text(append(append(row, el('span')), el('span')), state);
  overlay.replaceChildren();
  const pin = append(overlay, el('button', '', { 'data-comment-id': 'c1' }));
  addClass(pin, '__gust_pin');
  text(append(pin, el('span', '__gust_pin_count')), '1');
  return { item: item, row: row, badge: badge, pin: pin };
}
let thread = renderThread('Draft');

/* Ctrl+click a thread row: real text, real HTML, and a selector that does not
   depend on the state label the row is showing. */
gustSelection = true;
const rowLocator = JSON.parse(locatorFor(thread.row, { x: 0.5, y: 0.5 }));
assert(rowLocator.selector === '#__gust_comments div[data-comment-id="c1"] > button', 'a thread row selector is anchored on the thread id, got ' + rowLocator.selector);
assert(rowLocator.gust === true && rowLocator.identity === 'c1', 'a thread row comment records the thread id');
assert(rowLocator.text.includes('Make the badge bigger') && rowLocator.text.includes('Draft'), 'a thread row comment keeps the row text, got ' + rowLocator.text);
assert(rowLocator.confidence === 'high' && rowLocator.matches === 1, 'a thread row comment resolves to exactly one node');
const rowHTML = safeOuterHTML(thread.row);
assert(rowHTML.includes('Make the badge bigger') && rowHTML.includes('Draft') && rowHTML.includes('</button>'), 'a thread row comment keeps the row HTML');

/* The row re-renders when the thread changes state. The pin must follow the
   thread id, not the text that was on screen when the comment was made. */
thread = renderThread('In progress');
assert(matchingElement({ locator: JSON.stringify(rowLocator) }) === thread.row, 'the pin follows the re-rendered row after its state label changed');
thread = renderThread('Submitted');
assert(matchingElement({ locator: JSON.stringify(rowLocator) }) === thread.row, 'the pin survives a submitted label too');
thread = renderThread('Draft');

/* Ctrl+click a thread pin: the count inside it changes with every reply, so the
   comment must not depend on it either. */
const pinLocator = JSON.parse(locatorFor(thread.pin, { x: 0.5, y: 0.5 }));
assert(pinLocator.selector === '#__gust_pin_overlay button[data-comment-id="c1"]', 'a pin selector is anchored on the thread id, got ' + pinLocator.selector);
assert(pinLocator.gust === true && pinLocator.identity === 'c1', 'a pin comment records the thread id');
assert(pinLocator.text === '1', 'a pin comment keeps the count it pointed at, got ' + pinLocator.text);
const pinHTML = safeOuterHTML(thread.pin);
assert(pinHTML.includes('__gust_pin') && pinHTML.includes('1'), 'a pin comment keeps the pin HTML');
thread = renderThread('Draft');
text(thread.pin.children[0], '4');
assert(matchingElement({ locator: JSON.stringify(pinLocator) }) === thread.pin, 'the pin survives its own count changing');

/* Ctrl+click the server log: the log text is the context, but a password input
   inside it is still dropped. */
const logLocator = JSON.parse(locatorFor(log, { x: 0.5, y: 0.5 }));
assert(logLocator.selector === '#__gust_panel details[data-name="Server"]', 'a log selector is anchored on the log name, got ' + logLocator.selector);
assert(logLocator.gust === true && logLocator.identity === 'Server', 'a log comment records the log name');
assert(logLocator.text.includes('listening on 127.0.0.1:8080'), 'a log comment keeps the log text, got ' + logLocator.text);
const logHTML = safeOuterHTML(log);
assert(logHTML.includes('listening on 127.0.0.1:8080'), 'a log comment keeps the log HTML');
assert(!logHTML.includes('hunter2') && !logHTML.includes('type="password"'), 'a log comment still drops password inputs');

/* The count badge has no stable id, so the comment relies on the panel's own
   markup - and must survive the number changing. */
const countLocator = JSON.parse(locatorFor(count, { x: 0.5, y: 0.5 }));
assert(countLocator.gust === true && countLocator.identity === '', 'a count badge comment is a Ctrl comment with no identity');
assert(countLocator.text === '1', 'a count badge comment keeps the count, got ' + countLocator.text);
assert(safeOuterHTML(count).includes('>1<'), 'a count badge comment keeps the badge HTML');
count.text = '12';
assert(matchingElement({ locator: JSON.stringify(countLocator) }) === count, 'the count badge pin survives the count changing');

/* Without Ctrl a thread row still captures nothing, and a stored comment from
   before this behaviour existed never resolves onto a Gust node. */
gustSelection = false;
assert(JSON.parse(locatorFor(thread.row, null)).text === '', 'without Ctrl a thread row still captures no text');
assert(safeOuterHTML(thread.row) === '', 'without Ctrl a thread row still captures no HTML');
assert(JSON.parse(locatorFor(thread.row, null)).gust === false, 'without Ctrl a thread row comment is not a Ctrl comment');
const legacy = JSON.parse(JSON.stringify(rowLocator));
legacy.gust = false;
legacy.identity = '';
assert(matchingElement({ locator: JSON.stringify(legacy) }) === null, 'a stored comment without the Ctrl flag never resolves onto a thread row');

/* Ctrl on the host page captures exactly what a plain click captures, and the
   injected widget is still stripped out of a page element's context. */
gustSelection = true;
const pageLocator = JSON.parse(locatorFor(hero.children[0], null));
assert(pageLocator.gust === false && pageLocator.identity === '', 'Ctrl on a page element is not a Ctrl comment');
assert(pageLocator.text === 'Hello', 'Ctrl on a page element keeps the page text, got ' + pageLocator.text);
assert(!safeOuterHTML(document.body).includes('__gust_widget'), 'Ctrl on body still strips the injected widget');
console.log('comment capture ok');
`

const commentGuardHarnessPrelude = `/* Just enough DOM for the real Element.closest()/matches() calls in the guard. */
function matchesSimple(el, part) {
  if (part === '*') return true;
  const parsed = /^([a-z]*)((?:[#.][\w-]+|\[[^\]]+\]|:nth-of-type\(\d+\))*)$/i.exec(part);
  if (!parsed) throw new Error('unsupported selector: ' + part);
  if (parsed[1] && el.tagName.toLowerCase() !== parsed[1].toLowerCase()) return false;
  for (const token of parsed[2].match(/[#.][\w-]+|\[[^\]]+\]|:nth-of-type\(\d+\)/g) || []) {
    if (token[0] === '#') { if (el.id !== token.slice(1)) return false; }
    else if (token[0] === '.') { if (!el.classList.contains(token.slice(1))) return false; }
    else if (token[0] === ':') {
      const at = el.parentElement ? Array.from(el.parentElement.children).filter((x) => x.tagName === el.tagName).indexOf(el) + 1 : 1;
      if (at !== Number(token.match(/^:nth-of-type\((\d+)\)$/)[1])) return false;
    } else {
      const body = token.slice(1, -1), eq = body.indexOf('=');
      const name = eq < 0 ? body : body.slice(0, eq);
      if (!el.hasAttribute(name)) return false;
      if (eq >= 0 && el.getAttribute(name) !== body.slice(eq + 1).replace(/^["']|["']$/g, '')) return false;
    }
  }
  return true;
}
function matchesList(el, selector) {
  return selector.split(',').some(function(part) {
    const steps = part.trim().split(/\s+/).filter(Boolean);
    let node = el, direct = true;
    for (let i = steps.length - 1; i >= 0; i--) {
      if (direct) { if (!node || !matchesSimple(node, steps[i])) return false; }
      else {
        while (node && !matchesSimple(node, steps[i])) node = node.parentElement;
        if (!node) return false;
      }
      if (i > 0) { direct = steps[i - 1] === '>'; if (direct) i--; node = node.parentElement; }
    }
    return true;
  });
}
const voids = ['input', 'br', 'img', 'hr', 'meta', 'link'];
class El {
  constructor(tag) {
    this.tagName = (tag || 'div').toUpperCase();
    this.nodeType = 1;
    this.id = '';
    this.attrs = {};
    this.classes = [];
    this.children = [];
    this.text = '';
    this.parentElement = null;
    this.listeners = {};
    this.classList = { contains: (name) => this.classes.includes(name) };
  }
  setAttribute(name, value) { this.attrs[name] = String(value); if (name === 'id') this.id = String(value); }
  getAttribute(name) { return name in this.attrs ? this.attrs[name] : (name === 'id' && this.id ? this.id : null); }
  hasAttribute(name) { return this.getAttribute(name) !== null; }
  removeAttribute(name) { delete this.attrs[name]; }
  get attributes() { return Object.keys(this.attrs).map((name) => ({ name: name, value: this.attrs[name] })); }
  appendChild(child) { child.parentElement = this; this.children.push(child); return child; }
  remove() {
    if (!this.parentElement) return;
    const at = this.parentElement.children.indexOf(this);
    if (at >= 0) this.parentElement.children.splice(at, 1);
    this.parentElement = null;
  }
  replaceChildren() { this.children = []; }
  cloneNode() {
    const copy = new El(this.tagName);
    copy.id = this.id;
    copy.attrs = Object.assign({}, this.attrs);
    copy.classes = this.classes.slice();
    copy.text = this.text;
    this.children.forEach((child) => copy.appendChild(child.cloneNode(true)));
    return copy;
  }
  addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); }
  matches(selector) { return matchesList(this, selector); }
  closest(selector) { for (let n = this; n; n = n.parentElement) if (matchesList(n, selector)) return n; return null; }
  descendants() { return this.children.reduce((all, child) => all.concat([child], child.descendants()), []); }
  querySelectorAll(selector) { return this.descendants().filter((node) => matchesList(node, selector)); }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  get textContent() { return this.text + this.children.map((child) => child.textContent).join(''); }
  set textContent(value) { this.text = value; this.children = []; }
  get innerText() { return this.textContent; }
  get innerHTML() { return this.text + this.children.map((child) => child.outerHTML).join(''); }
  get outerHTML() {
    const tag = this.tagName.toLowerCase();
    const attrs = Object.keys(this.attrs).map((name) => ' ' + name + '="' + this.attrs[name] + '"').join('');
    if (voids.includes(tag)) return '<' + tag + attrs + '>';
    return '<' + tag + attrs + '>' + this.innerHTML + '</' + tag + '>';
  }
}
const document = {
  body: new El('body'),
  documentElement: new El('html'),
  listeners: {},
  createElement(tag) { return new El(tag); },
  addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
  querySelectorAll(selector) { return [this.documentElement, this.body].flatMap((root) => root.querySelectorAll(selector)); },
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; },
};
const window = {};
const selfDev = true;
const location = { pathname: '/' };
let selecting = true;
let hoverPath = [];
let highlighted = null;
let chooseCalls = 0;
let gustSelection = false;
function updateHoverPath(target, ctrlKey) { hoverPath = [target]; return true; }
function chooseSelection() { chooseCalls++; }
function setHighlight(el) { highlighted = el; }
function updateCommentUI() {}
function el(tag, id, attrs) {
  const node = new El(tag);
  if (id) node.id = id;
  Object.keys(attrs || {}).forEach(function(name) { node.setAttribute(name, attrs[name]); });
  return node;
}
function append(parent, child) { parent.appendChild(child); return child; }
function addClass(node, name) { node.classes.push(name); node.setAttribute('class', node.classes.join(' ')); return node; }
function text(node, value) { node.text = value; return node; }
/* Browser order: capture handlers on document first, then the target's own
   listeners, unless a capture handler stopped the event. */
function send(type, target, ctrlKey) {
  const event = {
    target: target,
    ctrlKey: !!ctrlKey,
    stopped: false,
    preventDefault() { this.defaultPrevented = true; },
    stopPropagation() { this.stopped = true; },
    stopImmediatePropagation() { this.stopped = true; },
  };
  for (const fn of (document.listeners[type] || []).slice()) {
    fn(event);
    if (event.stopped) break;
  }
  if (!event.stopped) for (const fn of (target.listeners[type] || []).slice()) fn(event);
  return event;
}
function assert(condition, message) { if (!condition) throw new Error(message); }
`

const commentGuardHarnessChecks = `
/* The real nesting: the info panel and the comment list are both children of
   #__gust_widget, the bubble and comment badge hang off the body, and pins,
   popover and breadcrumbs carry [data-gust-overlay]. */
const widget = append(document.body, el('div', '__gust_widget'));
const panel = append(widget, el('div', '__gust_panel'));
const comments = append(widget, el('div', '__gust_comments'));
const list = append(comments, el('div', '', { 'data-comments': '' }));

/* A plain page element is a normal comment target, with or without Ctrl. */
const hero = append(document.body, el('h1'));
const heroWord = append(hero, el('span'));
send('click', heroWord);
assert(!blockedCommentTarget(heroWord, false), 'a plain page element is not blocked');
assert(chooseCalls === 1, 'a plain page element opens the comment editor');
send('click', heroWord, true);
assert(chooseCalls === 2, 'Ctrl opens the comment editor on a page element too');

/* Ctrl selects panel chrome; a plain click does not. */
const label = append(append(panel, el('div')), el('span'));
send('click', label, true);
assert(!blockedCommentTarget(label, true), 'Ctrl unlocks panel chrome');
assert(chooseCalls === 3, 'Ctrl selects panel chrome in comment mode');
send('click', label, false);
assert(chooseCalls === 3, 'without Ctrl panel chrome stays blocked');
assert(blockedCommentTarget(label, false), 'panel chrome is blocked with no modifier');
assert(isSelfDevPanel(label) && !isPrivatePanelNode(label), 'panel chrome is a self-dev node');

/* Ctrl selects the parts that used to be private - thread rows, pins, the
   popover, the breadcrumbs, the Gust icon, the server log. Without Ctrl they
   stay unselectable, so their own click handlers still run. */
const thread = append(list, el('button', '', { 'data-comment-id': 'c1' }));
let threadClicks = 0;
thread.addEventListener('click', function() { threadClicks++; });
send('click', thread, true);
assert(chooseCalls === 4, 'Ctrl selects a thread row');
assert(isPrivatePanelNode(thread), 'a thread row is a private panel node');
send('click', thread, false);
assert(chooseCalls === 4, 'without Ctrl a thread row stays blocked');
assert(threadClicks === 1, 'without Ctrl a thread row still gets its own click');
/* Ctrl also builds a hover path for a private node, bounded by the comment list
   instead of running up the whole page. */
const hoverThread = meaningfulPath(thread, true);
assert(hoverThread.path.length > 0, 'Ctrl builds a hover path for a thread row');
assert(hoverThread.path[hoverThread.path.length - 1] === comments, 'a thread row path stops at the comment list');
assert(hoverThread.path[hoverThread.guessedIndex] === thread, 'a thread row defaults to the row itself');
assert(meaningfulPath(thread, false).path.length === 0, 'without Ctrl a thread row has no path at all');
const overlay = append(document.documentElement, el('div', '', { 'data-gust-overlay': '' }));
const pin = append(overlay, el('button'));
pin.classes = ['__gust_pin'];
send('click', pin, true);
assert(chooseCalls === 5, 'Ctrl selects a thread pin');
send('click', pin, false);
assert(chooseCalls === 5, 'without Ctrl a thread pin stays blocked');
const popover = append(overlay, el('div', '__gust_comment_popover'));
const crumbs = append(overlay, el('div', '__gust_selected_path'));
send('click', append(popover, el('button')), true);
send('click', append(crumbs, el('span')), true);
assert(chooseCalls === 7, 'Ctrl selects the popover and the breadcrumbs');
send('click', append(popover, el('button')));
send('click', append(crumbs, el('span')));
assert(chooseCalls === 7, 'without Ctrl the popover and breadcrumbs stay blocked');
const icon = append(widget, el('div', '__gust_icon'));
send('click', icon, true);
assert(chooseCalls === 8, 'Ctrl selects the Gust icon');
send('click', icon);
assert(chooseCalls === 8, 'without Ctrl the Gust icon stays blocked');
const log = append(panel, el('details'));
log.classes = ['__gust_log'];
send('click', append(log, el('pre')), true);
assert(chooseCalls === 9, 'Ctrl selects the server log');
send('click', append(log, el('pre')));
assert(chooseCalls === 9, 'without Ctrl the server log stays blocked');

/* The bubble is not mounted any more, but the guard must still know the id.
   A plain click on it is never a comment target, so its own click handler - the
   harness stands one in for the commented-out toolbox toggle - still runs. Ctrl
   selects it like any other element instead, which is the point of Ctrl. */
const bubble = append(document.body, el('button', '__gust_bubble'));
let toolboxToggles = 0;
bubble.addEventListener('click', function() { toolboxToggles++; });
const mark = append(bubble, el('span', '__gust_bubble_mark'));
assert(isGustNode(bubble), 'the toolbox bubble is a Gust node');
assert(blockedCommentTarget(bubble, false), 'the toolbox bubble is a blocked comment target');
assert(blockedCommentTarget(mark, false), 'anything inside the bubble is blocked with its parent');
send('click', bubble);
assert(chooseCalls === 9, 'a plain click on the bubble never opens the comment editor');
assert(toolboxToggles === 1, 'a plain click on the bubble opens the toolbox in comment mode');
send('click', bubble, true);
assert(chooseCalls === 10, 'Ctrl selects the bubble like any other element');
assert(toolboxToggles === 1, 'Ctrl+click selects the bubble instead of toggling the toolbox');
hoverPath = ['stale'];
send('mousemove', bubble);
assert(hoverPath.length === 0, 'hovering the bubble clears the comment path');
send('mousemove', bubble, true);
assert(hoverPath.length === 1 && hoverPath[0] === bubble, 'Ctrl hovering the bubble builds a path');
send('mousemove', heroWord);
assert(hoverPath.length === 1 && hoverPath[0] === heroWord, 'hovering a page element still builds a path');

/* The comment bubble and unread badge keep working the other way round. */
const commentBubble = append(document.body, el('button', '__gust_comment_bubble'));
const unreadBadge = append(document.body, el('button', '__gust_comment_unread_badge'));
assert(blockedCommentTarget(commentBubble, false), 'the comment bubble is a blocked comment target');
assert(blockedCommentTarget(unreadBadge, false), 'the unread badge is a blocked comment target');
send('click', commentBubble);
send('click', unreadBadge);
assert(chooseCalls === 10, 'plain clicks on the comment bubble and the badge open nothing');
send('click', commentBubble, true);
send('click', unreadBadge, true);
assert(chooseCalls === 12, 'Ctrl selects the comment bubble and the unread badge');
console.log('comment target guard ok');
`

func TestProxyInjectedScriptContent(t *testing.T) {
	script := reloadScript(12, 8765)
	checks := []string{
		`<script id="__gust_reload">`,
		`let lastVersion = 12;`,
		`/__gust/ws`,
		`new WebSocket(url)`,
		`location.reload()`,
		`setTimeout(connect, retry)`,
		`__gust_error`,
		`position:fixed;top:0`,
		`z-index:2147483646`,
		`z-index:2147483647`,
		`__gust_icon`,
		`position:fixed;right:8px;top:8px`,
		`__gust_icon_online`,
		`__gust_icon_offline`,
		`__gust_icon_failing`,
		`let connectionAttempted = false;`,
		`gustIcon.classList.toggle("__gust_icon_offline", !connected && connectionAttempted)`,
		`connected = false; connectionAttempted = true; applyIconState()`,
		`#__gust_icon{position:relative;width:28px;height:28px;color:#f9bb71;opacity:1;border-radius:10px;transition:background .15s ease,color .15s ease,opacity .15s ease}`,
		`viewBox="0 0 256 256"`,
		`#__gust_icon::after{content:"";position:absolute;right:-2px;bottom:-2px;width:8px;height:8px;border:2px solid #f9bb71;border-radius:50%;background:#24180f;`,
		`#__gust_icon.__gust_icon_online::after{background:#22c55e;border-color:#24180f}`,
		`#__gust_icon.__gust_icon_offline::after{background:#dc2626;border-color:#24180f}`,
		`#__gust_icon.__gust_icon_failing::after{background:#f59e0b;border-color:#24180f}`,
		`#__gust_panel{top:36px;color-scheme:dark;background:#ffffff0d;color:#fff7e9;border:1px solid #ffffff24;border-radius:1.2rem;backdrop-filter:blur(12px);`,
		`#__gust_comments textarea,[data-editor] textarea{background:#352416;color:#fff7e9;`,
		`#__gust_comment_editor{color-scheme:dark;background:#24180f;color:#fff7e9;`,
		`opacity:.45`,
		`#22c55e`,
		`#dc2626`,
		`#f59e0b`,
		`__gust_notice`,
		`__gust_group`,
		`function taskGroup(`,
		`__gust_wide`,
		`__gust_log`,
		`data-name=`,
		`function logSection(`,
		`<pre><code>`,
		`background:transparent`,
		`/__gust/status`,
		`gustProcess`,
		`msg.type === "notice"`,
		`gustInfo.notice`,
		`function esc(`,
		`cursor:pointer`,
		`__gust_pinned`,
		`localStorage.getItem("__gust_pinned")`,
		`localStorage.setItem("__gust_pinned"`,
		`const gustAppPort = 8765;`,
		`__gust_panel`,
		`/__gust/info`,
		`__gust_widget`,
		`#__gust_widget.__gust_open #__gust_panel`,
		`__gust_dot`,
		`font:14px/1.6`,
		`Proxying`,
		`Reloaded`,
		`Status`,
		`Version`,
		`Auto-reload`,
		`Trigger`,
		`Ready In`,
		`pinned`,
		`127.0.0.1:`,
		`__gust_debug_style`,
		`gust-debug`,
		`outline-offset: -1px`,
		`outline-offset:2px`,
		`commentToolbar) gustPanel.appendChild(commentToolbar)`,
		`#__gust_comment_toolbar{display:flex;`,
		`#__gust_widget.__gust_open.__gust_commenting #__gust_comments{display:block}`,
		`gustWidget.classList.toggle("__gust_commenting",active)`,
		`M2.992 16.342`,
		`textContent="Comment Mode"`,
		`t.setAttribute("aria-label",label)`,
		`document.addEventListener("mouseout",function(e){if(selecting&&!e.relatedTarget)`,
		`selectedElement=hoverPath[selectedIndex]`,
		`const el=selectedElement; const text=textarea.value.trim();`,
		`function updateEditorPosition()`,
		`editorAnchor={x:e.clientX,y:e.clientY}`,
		`if(left+width>innerWidth-margin)left=editorAnchor.x-width-gap`,
		`if(top+height>innerHeight-margin)top=editorAnchor.y-height-gap`,
		`position:fixed;display:none;z-index:2147483647`,
		`font:13px/1.5 ui-monospace`,
		`max-height:calc(100vh - 24px)`,
		`if(blockedCommentTarget(e.target,e.ctrlKey)){hoverPath=[];setHighlight(null);updateCommentUI();return;}`,
		`window.addEventListener("scroll",function(){if(editorOpen)updateEditorPosition();},true)`,
		`function closeCommentMode()`,
		`function meaningfulPath(`,
		`for (let n = el; n && path.length < 12; n = n.parentElement)`,
		`if(!selector&&(el===document.body||el===document.documentElement))selector=tag`,
		`return {path:path, guessedIndex:Math.max(0,path.indexOf(guess))}`,
		`function updateHoverPath(target, ctrlKey){`,
		`const commentIconSvg='<svg`,
		`if (pinned || hovering) {`,
		`const commentAddIconSvg='<svg`,
		`function mountCommentBubble(){`,
		`commentBubble.id="__gust_comment_bubble";`,
		`commentBubble.addEventListener("click",function(e){e.stopPropagation();beginCommentTool();});`,
		`#__gust_comment_bubble{position:fixed;right:14px;top:50%;`,
		`#__gust_comment_bubble[aria-pressed=true]{background:#f9bb71;`,
		`#__gust_icon.__gust_pinned{background:#49301c;color:#fff7e9}`,
		`const active=selecting||editorOpen,label=active?"Exit comment mode":"Add comment",toggleTitle=`,
		`function beginCommentTool(){if(selecting||editorOpen){closeCommentMode();return;}`,
		`function elementSnippet(`,
		`function fullAncestry(`,
		`function renderPath(`,
		`function scheduleRenderPath(`,
		`target.matches("html,head,body")?"":elementSnippet(target)`,
		`ancestors.forEach(function(name){addPart(name,"__gust_path_ancestor");})`,
		`#__gust_selected_path .__gust_path_trail{display:flex;flex-wrap:wrap`,
		`max-width:calc(100vw - 24px)`,
		`crumbs.replaceChildren(trail)`,
		`text-shadow:0 1px 3px #000`,
		`crumbs.id="__gust_selected_path"`,
		`document.documentElement.appendChild(crumbs)`,
		`document.getElementById("__gust_selected_path")`,
		`#__gust_selected_path{position:fixed;bottom:16px`,
		`#__gust_selected_path[hidden]{display:none}`,
		`#__gust_selected_path .__gust_path_current{`,
		`document.documentElement.classList.toggle("__gust_selecting",selecting)`,
		`const commentCursor="url('data:image/svg+xml,"+encodeURIComponent(commentIconSvg.replace("currentColor","#f59e0b"))`,
		`html.__gust_selecting,html.__gust_selecting *{cursor:"+commentCursor+"!important}`,
		`html.__gust_selecting [data-gust-overlay]`,
		`function currentTargetEl()`,
		`function resumeSelection(){`,
		`refreshComments();resumeSelection();`,
		`const box=document.querySelector("[data-editor] textarea");if(box)box.focus();`,
		"renderCommentState();\n  updateCommentUI();",
		`document.addEventListener("click"`,
		`e.preventDefault();e.stopPropagation();e.stopImmediatePropagation();`,
		`chooseSelection(e);`,
		`selectedPoint=r.width>0&&r.height>0?`,
		`locator:locatorFor(el,selectedPoint)`,
		`const x=p&&Number.isFinite(p.x)`,
		`const y=p&&Number.isFinite(p.y)`,
		`new ResizeObserver(schedulePinReposition)`,
		`if(pinSizeObserver)pinSizeObserver.observe(el)`,
		`window.addEventListener("scroll",schedulePinReposition,true)`,
		`window.addEventListener("resize",schedulePinReposition)`,
		`requestAnimationFrame(function(){pinPositionFrame=0;repositionPins();})`,
		`function repositionPins(){`,
		`const el=c&&c.path===location.pathname&&matchingElement(c);`,
		`replace(/\s+/g," ")`,
		`function locatorFor(`,
		`function safeOuterHTML(`,

		`fetch("/__gust/comments"`,
		`method:"POST"`,
		`location.pathname`,
		`JSON.stringify({selector:selector,tag:tag,text:text,confidence:`,
		`__gust_pin`,
		`commentUI.append(status,listHeader,submitResult,pollError,list,autoLabel)`,
		`.__gust_autosubmit{display:flex`,
		`.__gust_autosubmit input[type=checkbox]{appearance:none`,
		`listHeader.append(listTitle,count,submit)`,
		`empty.textContent="No open comments yet."`,
		`__gust_comment_text{display:-webkit-box`,
		`__gust_comments_header{display:flex`,
		`auto.checked=true`,
		`e.key==="Enter"&&e.ctrlKey`,
		`sessionStorage.getItem("__gust_comment_mode")`,
		`sessionStorage.setItem("__gust_comment_mode", (selecting||editorOpen) ? "1" : "0")`,
		`selecting=loadCommentMode(); createCommentUI()`,
		`setHighlight(null);saveCommentMode();updateCommentUI();`,
		`/__gust/comments/submit?id=`,
		`active.slice().reverse().forEach(function(c)`,
		`summary.textContent=finished+" finished"`,
		`remove.setAttribute("aria-label","Remove draft")`,
		`method:"DELETE"`,
		`close.addEventListener("click",function(){textarea.value="";result.textContent="";resumeSelection();})`,
		`seen:"In progress"`,
		`review:"Review"`,
		`function threadUnread(c){`,
		`localStorage.getItem("__gust_thread_seen_"+id)`,
		`localStorage.setItem("__gust_thread_seen_"+c.id`,
		`unread.className="__gust_comment_unread"`,
		`pin.className="__gust_pin __gust_pin_"+c.state+(unread?" __gust_pin_unread":"")`,
		`.__gust_pin_count{position:absolute;left:50%;top:50%;transform:translate(-50%,-50%);`,
		`.__gust_pin_number{color:#fff;font:700 9px/1 system-ui,sans-serif}`,
		`.__gust_pin_unread::after{content:'';position:absolute`,
		`animation:__gust_pin_ring 1.4s ease-out infinite`,
		`@media (prefers-reduced-motion:reduce){.__gust_pin_unread::after{animation:none`,
		`if(c.state==="seen"){const spinner=document.createElement("span")`,
		`spinner.setAttribute("aria-hidden","true")`,
		`animation:__gust_comment_spin .9s linear infinite`,
		`prefers-reduced-motion:reduce`,
		`editor.append(close,title,textarea,save,hint,result)`,
		`replace(/\s+/g," ")`,
		`Comment sync failed: `,
		`open.title=onPage?`,
		`submit.textContent="Submit "+created`,
		`data-comment-id`,
		`pointer-events:auto`,
		`fetch("/__gust/comments/submit"`,
		`function refreshComments()`,
		`setInterval(refreshComments,3000)`,
		`l.confidence!=="high"||l.matches!==1`,
		`replace(/\s+/g," ");if(!actual.includes(l.text))return null;`,
		`c.state==="created"||c.state==="submitted"||c.state==="seen"||c.state==="review"`,
		`submitResult.dataset.submitResult`,
		`missing.textContent="Not found"`,
	}
	for _, check := range checks {
		if !strings.Contains(script, check) {
			t.Fatalf("script missing %q in %s", check, script)
		}
	}
	for _, removed := range []string{
		"Shrink",
		"Expand",
		"Choose this element",
		"Cancel",
		"Change selection",
		"Selected element",
		"commentToggleButton",
		"__gust_comment_toggle",
		"const add=document.createElement(\"button\")",
		"add.addEventListener(\"click\",beginSelection)",
		"function beginSelection(",
	} {
		if strings.Contains(script, removed) {
			t.Errorf("script still contains removed comment-mode UI %q", removed)
		}
	}
}

// TestProxyPinOpensFloatingCommentPanel checks that clicking a comment pin
// opens a floating panel anchored to the pin instead of the side panel, that
// the panel tracks comment state changes until the comment is resolved, and
// that the existing lifecycle actions are still wired up.
func TestProxyPinOpensFloatingCommentPanel(t *testing.T) {
	script := reloadScript(12, 8765)
	checks := []string{
		`function openCommentPopover(id){`,
		`commentPopover.id="__gust_comment_popover"`,
		`commentPopover.dataset.gustOverlay=""`,
		`commentPopover.setAttribute("role","dialog")`,
		`function commentPopoverEl(){`,
		`function positionCommentPopover(){`,
		`function closeCommentPopover(){`,
		`function removePopoverDraft(){`,
		`function sendPopoverReply(){`,
		`function resolvePopoverThread(){`,
		`function renderPopoverReplies(c){`,
		`function renderCommentPopover(){`,
		`function commentStateLabel(state)`,
		`function threadUnread(c){`,
		`function markThreadSeen(c){`,
		`badge.className="__gust_popover_badge __gust_popover_badge_"+c.state`,
		`pin.addEventListener("click",function(e){e.preventDefault();e.stopPropagation();openCommentPopover(c.id);});`,
		`if(popoverCommentId===id&&commentPopover&&!commentPopover.hidden){closeCommentPopover();return;}`,
		`else if(popoverCommentId)closeCommentPopover();`,
		"renderCommentPopover();\n}\nfunction refreshComments(){",
		"positionCommentPopover();\n}",
		`if(!popoverCommentId||!commentPopover||commentPopover.hidden)return;`,
		// The panel survives created -> submitted -> seen -> review and only
		// closes when the comment is resolved.
		`if(!c||c.state==="done"){closeCommentPopover();return;}`,
		// Human replies and resolves post to dedicated routes.
		`fetch("/__gust/comments/"+encodeURIComponent(id)+"/reply"`,
		`fetch("/__gust/comments/"+encodeURIComponent(id)+"/resolve"`,
		// In-flight reply/resolve callbacks are guarded so a late response
		// never mutates a popover the user has since switched or reopened.
		`if(token!==popoverDeleteToken)return;`,
		`const token=++popoverDeleteToken;`,
		`if(popoverCommentId===id&&(popoverReplyDraft||"").trim()===text){`,
		// Drafts hide Resolve because the server rejects resolving a created comment.
		`parts.resolve.hidden=c.state==="created";`,
		// Comment refreshes are generation-guarded so a slow older GET cannot
		// overwrite newer state after a reply or resolve.
		`const generation=++commentFetchGeneration;`,
		`if(generation!==commentFetchGeneration)return;`,
		`if(popoverReplyId!==id){popoverReplyId=id;popoverReplyDraft="";}`,
		`markThreadSeen(commentState.find(function(x){return x.id===id;}));`,
		`renderPopoverReplies(c);`,
		`if(parts.reply.value!==popoverReplyDraft)parts.reply.value=popoverReplyDraft;`,
		// Rebuilding over a detached badge must drop the stale parent so the
		// floating panel never leaves a duplicate id in the document.
		`if(commentPopover&&commentPopover.isConnected)commentPopover.remove();`,
		// A successful DELETE closes the popover before the follow-up refresh,
		// so a failing refresh cannot leave the deleted draft open.
		`if(popoverCommentId===c.id)closeCommentPopover();`,
		`commentState.filter(c=>c.path===location.pathname&&(c.state==="created"||c.state==="submitted"||c.state==="seen"||c.state==="review"))`,
		// Existing actions stay available from the panel.
		`fetch("/__gust/comments/"+encodeURIComponent(c.id),{method:"DELETE"})`,
		`parts.remove.textContent=popoverDeletePending?"Removing\u2026":"Remove draft"`,
		`locate.addEventListener("click",function(){locateComment(commentState.find(function(x){return x.id===popoverCommentId;}));})`,
		`locateComment(commentState.find(function(x){return x.id===id;}))`,
		// The panel is built once and re-rendered in place so polls are stable.
		`commentPopover.append(head,text,meta,replies,replybox,actions,error)`,
		`popoverParts={badge:badge,dismiss:dismiss,text:text,path:path,missing:missing,replies:replies,replybox:replybox,reply:reply,send:send,actions:actions,locate:locate,resolve:resolve,remove:remove,error:error}`,
		`if(key===popoverRenderKey){positionCommentPopover();return;}`,
		`parts.remove.disabled=popoverDeletePending;`,
		`parts.error.hidden=!popoverActionError;`,
		`parts.error.textContent=popoverActionError||""`,
		// Styling for the floating panel.
		`#__gust_comment_popover{position:fixed;z-index:2147483647;`,
		`#__gust_comment_popover[hidden]{display:none}`,
		`#__gust_comment_popover .__gust_popover_badge_created{color:#fbbf24}`,
		`#__gust_comment_popover .__gust_popover_badge_review{color:#f9a8d4}`,
		`#__gust_comment_popover .__gust_popover_replybox{`,
		`#__gust_comment_popover .__gust_popover_error{`,
	}
	for _, check := range checks {
		if !strings.Contains(script, check) {
			t.Errorf("floating comment panel missing %q", check)
		}
	}
	if strings.Contains(script, `syncPanel();focusComment(c.id)`) {
		t.Error("pin click still opens the side panel")
	}
	if strings.Contains(script, `commentPopover.replaceChildren()`) {
		t.Error("popover refresh still tears down its DOM")
	}
	if !strings.Contains(script, `open.addEventListener("click",function(){focusComment(c.id);});row.appendChild(open);`) {
		t.Error("side panel rows should still focus their comment")
	}
}

// TestCommentPinShowsMessageCountWhenNodeAvailable runs the real pin rendering
// against a tiny DOM stub. The count is the thread's own messages, drawn inside
// the pin dot, and it must be there with the popover closed and stay the same
// when the popover opens or closes.
func TestCommentPinShowsMessageCountWhenNodeAvailable(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := reloadScript(12, 8765)
	messagesStart := strings.Index(script, "function threadMessages(c){")
	messagesEnd := strings.Index(script, "function threadLastSeen(id){")
	pinsStart := strings.Index(script, "const overlay=ensurePinOverlay();")
	pinsEnd := strings.Index(script, "renderCommentPopover();\n}\nfunction refreshComments()")
	if messagesStart < 0 || messagesEnd < messagesStart || pinsStart < 0 || pinsEnd < pinsStart {
		t.Fatal("could not extract the pin message count")
	}
	pins := "function renderPins(){\n" + script[pinsStart:pinsEnd] + "}\n"
	harness := pinCountHarnessPrelude + script[messagesStart:messagesEnd] + pins + pinCountHarnessChecks
	file := t.TempDir() + "/pin-count.js"
	if err := os.WriteFile(file, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("pin message count behavior: %v\n%s", err, output)
	}
}

const pinCountHarnessPrelude = `class El {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.attrs = {};
    this.dataset = {};
    this.style = {};
    this.listeners = {};
    this.className = '';
    this.textContent = '';
    this.title = '';
    this.type = '';
    this.hidden = false;
    this.isConnected = true;
  }
  appendChild(c) { this.children.push(c); return c; }
  replaceChildren(...cs) { this.children = []; for (const c of cs) this.appendChild(c); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  addEventListener(t, f) { this.listeners[t] = f; }
}
const document = { createElement: t => new El(t), documentElement: new El('html') };
const location = { pathname: '/' };
let commentState = [];
let popoverCommentId = null;
let pinSizeObserver = null;
let pinOverlay = new El('div');
let unread = false;
function ensurePinOverlay() { return pinOverlay; }
function matchingElement() { return new El('div'); }
function pinPosition() { return { left: 10, top: 10 }; }
function threadUnread() { return unread; }
function renderCommentPopover() {}
function openCommentPopover(id) { popoverCommentId = id; renderPins(); }
function closePopover() { popoverCommentId = null; renderPins(); }
function assert(cond, msg) { if (!cond) throw new Error(msg); }
function countOf(pin) { return pin.children[0].children[0].textContent; }
`

const pinCountHarnessChecks = `
popoverCommentId = null;
unread = false;
commentState = [
  { id: 't1', path: '/', state: 'review', text: 'on the dot', messages: [{ author: 'human', text: 'first' }, { author: 'agent', text: 'second' }] },
  { id: 't2', path: '/', state: 'created', text: 'fresh', messages: [] },
  { id: 't3', path: '/other', state: 'review', text: 'elsewhere', messages: [{}, {}, {}] },
];
renderPins();
assert(pinOverlay.children.length === 2, 'only threads on this page get a pin');
const pin = pinOverlay.children[0];
assert(pin.dataset.commentId === 't1', 'pins keep their thread id');
assert(pin.className === '__gust_pin __gust_pin_review', 'the review pin keeps its state class');
// Default state: the dot is on the page and the popover is closed, so the count
// has to be readable right now.
assert(pin.children.length === 1, 'the pin holds the count dot and nothing else');
assert(pin.children[0].className === '__gust_pin_count', 'the count is drawn inside the pin dot');
assert(pin.children[0].attrs['aria-hidden'] === 'true', 'the drawn count is hidden from assistive tech');
assert(pin.children[0].children.length === 1, 'the dot holds the number');
assert(pin.children[0].children[0].className === '__gust_pin_number', 'the number has its own hook');
assert(countOf(pin) === '3', 'a thread with two replies shows three messages inside the dot');
assert(pin.attrs['aria-label'] === 'review comment, 3 messages', 'the pin names the count for assistive tech');
assert(pin.title === 'review comment — 3 messages — click to inspect', 'the tooltip carries the count too');
// One number, one meaning: each thread counts only its own messages.
assert(countOf(pinOverlay.children[1]) === '1', 'a single-message thread shows one, never zero');
assert(pinOverlay.children[1].attrs['aria-label'] === 'created comment, 1 message', 'one message reads as singular');
assert(pinOverlay.children[1].className === '__gust_pin __gust_pin_created', 'the draft pin keeps its state class');
// Opening and closing the popover cannot move the number.
pin.listeners['click']({ preventDefault() {}, stopPropagation() {} });
assert(popoverCommentId === 't1', 'clicking the pin opens its popover');
const opened = pinOverlay.children[0];
assert(countOf(opened) === '3', 'opening the popover keeps the number');
closePopover();
assert(popoverCommentId === null, 'closing the popover clears the selection');
assert(countOf(pinOverlay.children[0]) === '3', 'closing the popover keeps the number');
// A reply lands in the thread, so the dot grows with the thread.
commentState[0].messages.push({ author: 'agent', text: 'third' });
renderPins();
assert(countOf(pinOverlay.children[0]) === '4', 'a new reply shows up in the dot');
commentState = [];
renderPins();
assert(pinOverlay.children.length === 0, 'no threads means no pins and no stray counts');
console.log('pin message count ok');
`

func TestCommentTargetIconInFloatingThreadPanel(t *testing.T) {
	script := reloadScript(12, 8765)
	const targetIcon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256"><rect width="256" height="256" fill="none"/><line x1="128" y1="128" x2="224" y2="32" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="16"/><path d="M195.88,60.12a95.88,95.88,0,1,0,18.77,26.49" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="16"/><path d="M161.94,94.06a48,48,0,1,0,14,31.2" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="16"/></svg>`
	for _, fragment := range []string{
		`const commentTargetIconSvg='` + targetIcon + `';`,
		`function renderCommentTarget(container,target){`,
		`icon.className="__gust_popover_path_icon";icon.setAttribute("aria-hidden","true");icon.innerHTML=commentTargetIconSvg;`,
		`text.className="__gust_popover_path_text";text.textContent=target||"/";`,
		`container.replaceChildren(icon,text);`,
		`renderCommentTarget(parts.path,c.path||"/");`,
		`#__gust_comment_popover .__gust_popover_path{display:flex;align-items:center;gap:5px;flex:1 1 auto;min-width:0;overflow:hidden;direction:ltr;text-align:left}`,
		`#__gust_comment_popover .__gust_popover_path_icon{display:inline-flex;flex:none;width:14px;height:14px;color:#e0cfba}`,
		`#__gust_comment_popover .__gust_popover_path_icon svg{display:block;width:100%;height:100%}`,
		`#__gust_comment_popover .__gust_popover_path_text{display:block;flex:1 1 auto;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;direction:rtl;text-align:left}`,
	} {
		if !strings.Contains(script, fragment) {
			t.Errorf("floating thread target icon missing %q", fragment)
		}
	}
	// The requested follow-up replaces the first proposed icon; do not ship both.
	if strings.Contains(script, `x1="128" y1="232" x2="128" y2="200"`) {
		t.Error("floating thread target still contains the first proposed icon")
	}
}

// TestProxyFloatingCommentPanelBehaviorWhenNodeAvailable runs the extracted
// popover functions against a tiny DOM stub, so the pin-to-panel interaction is
// exercised at runtime rather than only checked as source text.
func TestProxyFloatingCommentPanelBehaviorWhenNodeAvailable(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := reloadScript(12, 8765)
	start := strings.Index(script, "function locateComment(c){")
	end := strings.Index(script, "function renderCommentState(){")
	if start < 0 || end < start {
		t.Fatal("could not extract floating comment panel functions")
	}
	harness := popoverHarnessPrelude + script[start:end] + popoverHarnessChecks
	file := t.TempDir() + "/popover.js"
	if err := os.WriteFile(file, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("floating comment panel behavior: %v\n%s", err, output)
	}
}

const popoverHarnessPrelude = `class El {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.attrs = {};
    this.dataset = {};
    this.style = {};
    this.listeners = {};
    this.hidden = false;
    this.className = '';
    this.textContent = '';
    this.type = '';
    this.isConnected = true;
    this.parentNode = null;
    this.removed = false;
    this.offsetWidth = 300;
    this.offsetHeight = 160;
  }
  appendChild(c) { this.children.push(c); if (c) c.parentNode = this; return c; }
  append(...cs) { for (const c of cs) if (c && typeof c === 'object') { this.children.push(c); c.parentNode = this; } }
  replaceChildren(...cs) { for (const c of this.children) if (c) c.parentNode = null; this.children = []; for (const c of cs) this.appendChild(c); }
  remove() {
    if (this.parentNode) {
      const i = this.parentNode.children.indexOf(this);
      if (i >= 0) this.parentNode.children.splice(i, 1);
      this.parentNode = null;
    }
    this.isConnected = false;
    this.removed = true;
  }
  setAttribute(k, v) { this.attrs[k] = v; }
  addEventListener(t, f) { this.listeners[t] = f; }
  focus() { document.activeElement = this; }
  contains(el) { if (el === this) return true; return this.children.some(c => c && c.contains && c.contains(el)); }
  getBoundingClientRect() { return { left: 100, right: 120, top: 100, bottom: 120, width: 20, height: 20 }; }
}
const document = { createElement: t => new El(t), documentElement: new El('html'), activeElement: null };
const commentTargetIconSvg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256"><rect width="256" height="256" fill="none"/><line x1="128" y1="128" x2="224" y2="32" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="16"/><path d="M195.88,60.12a95.88,95.88,0,1,0,18.77,26.49" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="16"/><path d="M161.94,94.06a48,48,0,1,0,14,31.2" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="16"/></svg>';
const location = { pathname: '/' };
const innerWidth = 1000;
const innerHeight = 800;
let commentState = [];
let commentUnreadBadge = null;
function threadUnread(c) {
  const messages = c && Array.isArray(c.messages) ? c.messages : [];
  const newest = messages.length ? messages[messages.length - 1] : null;
  return !!(newest && newest.author === 'agent');
}
function updateUnreadBadge() {}
let commentPopover = null;
let popoverCommentId = null;
let popoverAnchor = null;
let popoverParts = null;
let popoverRenderKey = '';
let popoverDeletePending = false;
let popoverActionError = '';
let popoverDeleteToken = 0;
let commentFetchGeneration = 0;
let popoverReplyDraft = '';
let popoverReplyId = null;
let popoverReplyPending = false;
let popoverResolvePending = false;
let pinOverlay = null;
let commentUI = null;
const localStorage = {
  store: {},
  getItem(k) { return Object.prototype.hasOwnProperty.call(this.store, k) ? this.store[k] : null; },
  setItem(k, v) { this.store[k] = String(v); },
  removeItem(k) { delete this.store[k]; },
};
function matchingElement() { return new El('div'); }
function setHighlight() {}
function refreshComments() {}
function assert(cond, msg) { if (!cond) throw new Error(msg); }
function pinFor(id) { const p = new El('button'); p.dataset.commentId = id; return p; }
`

const popoverHarnessChecks = `
commentState = [{ id: 'a', path: '/', text: 'first\nsecond', state: 'created', messages: [] }];
pinOverlay = new El('div');
pinOverlay.appendChild(pinFor('a'));
openCommentPopover('a');
assert(commentPopover && commentPopover.hidden === false, 'pin click opens the floating panel');
assert(popoverCommentId === 'a', 'panel remembers its comment id');
assert(commentPopover.children.length === 7, 'panel has header, text, meta, replies, reply box, actions, and error');
assert(commentPopover.children[0].children[0].textContent === 'Draft', 'created state renders the Draft badge');
assert(commentPopover.children[1].textContent === 'first\nsecond', 'multi-line text is preserved');
const targetPath = popoverParts.path;
assert(targetPath.children.length === 2, 'target renders an icon and a text label');
assert(targetPath.children[0].className === '__gust_popover_path_icon', 'target icon precedes the text');
assert(targetPath.children[0].attrs['aria-hidden'] === 'true', 'decorative target icon is hidden from assistive technology');
assert(targetPath.children[0].innerHTML === commentTargetIconSvg, 'target uses the requested second SVG');
assert(targetPath.children[1].className === '__gust_popover_path_text', 'target text has a dedicated truncation wrapper');
assert(targetPath.children[1].textContent === '/', 'target text remains the original path');
assert(commentPopover.children[3].children[0].textContent === 'No replies yet.', 'empty thread shows a placeholder');
const createdActions = commentPopover.children[5];
assert(createdActions.children[2].hidden === false, 'drafts expose Remove draft');
assert(createdActions.children[2].textContent === 'Remove draft', 'draft button keeps its label');
assert(createdActions.children[0].hidden === false, 'located comments expose Locate');
assert(createdActions.children[1].textContent === 'Resolve', 'threads expose Resolve');
assert(createdActions.children[1].hidden === true, 'drafts hide Resolve because the server rejects it');
assert(commentPopover.children[4].children[1].textContent === 'Send', 'threads expose a Send button');
openCommentPopover('a');
assert(commentPopover.hidden === true, 'clicking the same pin toggles the panel closed');
openCommentPopover('a');
commentState[0].state = 'review';
commentState[0].messages = [{ id: 'm1', author: 'agent', text: 'please check', createdAt: new Date().toISOString() }];
renderCommentPopover();
assert(commentPopover.hidden === false, 'panel stays open across a state change');
assert(commentPopover.children[0].children[0].textContent === 'Review', 'panel updates the state badge');
assert(commentPopover.children[3].children[0].children[0].children[0].textContent === 'Agent', 'agent replies are labelled');
assert(commentPopover.children[3].children[0].children[1].textContent === 'please check', 'reply text is rendered');
assert(commentPopover.children[5].children[2].hidden === true, 'non-drafts hide Remove draft');
assert(commentPopover.children[5].children[1].hidden === false, 'review threads expose Resolve');
commentState[0].state = 'done';
renderCommentPopover();
assert(commentPopover.hidden === true, 'resolving the comment closes the panel');
assert(popoverCommentId === null, 'resolution clears the selected comment');
commentState = [{ id: 'b', path: '/', text: 'x', state: 'submitted', messages: [] }];
pinOverlay.replaceChildren(pinFor('b'));
openCommentPopover('b');
assert(commentPopover.children[0].children[0].textContent === 'Submitted', 'submitted state renders the Submitted badge');
closeCommentPopover();
assert(commentPopover.hidden === true, 'close hides the panel');
// Unread tracking: only a newer agent message counts as unread.
const agentReply = { id: 'm9', author: 'agent', text: 'new', createdAt: new Date(Date.now() + 1000).toISOString() };
commentState = [{ id: 'u', path: '/', text: 'u', state: 'review', messages: [agentReply] }];
assert(threadUnread(commentState[0]) === true, 'newer agent reply is unread');
markThreadSeen(commentState[0]);
assert(threadUnread(commentState[0]) === false, 'marking the thread seen clears unread');
commentState[0].messages.push({ id: 'm10', author: 'human', text: 'mine', createdAt: new Date(Date.now() + 2000).toISOString() });
assert(threadUnread(commentState[0]) === false, 'a newer human reply is never unread');
// Clock skew: the seen marker stores the server timestamp, so a later agent
// reply is still unread even when the client clock runs ahead of the server.
const skewBase = Date.now();
commentState = [{ id: 'skew', path: '/', text: 'skew', state: 'review', messages: [{ id: 's1', author: 'agent', text: 'one', createdAt: new Date(skewBase - 5000).toISOString() }] }];
markThreadSeen(commentState[0]);
commentState[0].messages.push({ id: 's2', author: 'agent', text: 'two', createdAt: new Date(skewBase - 1000).toISOString() });
assert(threadUnread(commentState[0]) === true, 'later agent reply survives client clock skew');
console.log('floating comment panel ok');
`

// TestProxyFloatingCommentPanelRefreshStabilityWhenNodeAvailable exercises the
// floating panel across repeated refreshes: unchanged polls must reuse the same
// nodes and keep focus, DELETE failures must stay visible, and an in-flight
// remove must stay disabled and un-repeatable.
func TestProxyFloatingCommentPanelRefreshStabilityWhenNodeAvailable(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := reloadScript(12, 8765)
	start := strings.Index(script, "function locateComment(c){")
	end := strings.Index(script, "function renderCommentState(){")
	if start < 0 || end < start {
		t.Fatal("could not extract floating comment panel functions")
	}
	harness := popoverHarnessPrelude + script[start:end] + popoverStabilityChecks
	file := t.TempDir() + "/popover-stability.js"
	if err := os.WriteFile(file, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("floating comment panel refresh stability: %v\n%s", err, output)
	}
}

const popoverStabilityChecks = `
const fetchCalls = [];
let fetchMode = 'error';
let pendingResolve = null;
function fetch(url, opts) {
  fetchCalls.push({ url, opts });
  if (fetchMode === 'pending') return new Promise(resolve => { pendingResolve = resolve; });
  return Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({ error: { message: 'boom' } }) });
}
function tick() { return new Promise(resolve => setTimeout(resolve, 0)); }
(async function () {
  commentState = [{ id: 'f', path: '/', text: 'draft focus', state: 'created' }];
  pinOverlay = new El('div');
  pinOverlay.appendChild(pinFor('f'));
  openCommentPopover('f');
  const actions = commentPopover.children[5];
  const locate = actions.children[0];
  const remove = actions.children[2];
  assert(remove.hidden === false, 'draft exposes Remove draft');
  assert(locate.hidden === false, 'located draft exposes Locate');
  remove.focus();
  assert(document.activeElement === remove, 'Remove draft can take focus');

  // Unchanged poll refresh: same nodes, focus retained, no repaint.
  renderCommentPopover();
  assert(commentPopover.children[5] === actions, 'refresh keeps the actions container');
  assert(commentPopover.children[5].children[2] === remove, 'refresh keeps the Remove draft node');
  assert(document.activeElement === remove, 'refresh keeps focus on Remove draft');

  // Failed DELETE stays visible and re-enables the button.
  remove.listeners.click();
  await tick();
  assert(remove.disabled === false, 'failed delete re-enables Remove draft');
  assert(remove.textContent === 'Remove draft', 'failed delete keeps the button label');
  assert(commentPopover.children[6].hidden === false, 'failed delete shows the error area');
  assert(commentPopover.children[6].textContent === 'boom', 'error area carries the message');
  renderCommentPopover();
  assert(commentPopover.children[6].hidden === false, 'poll refresh preserves the visible delete error');
  assert(commentPopover.children[6].textContent === 'boom', 'poll refresh preserves the error text');

  // In-flight DELETE stays disabled, labelled, and cannot be repeated.
  fetchMode = 'pending';
  fetchCalls.length = 0;
  remove.listeners.click();
  assert(fetchCalls.length === 1, 'pending delete issues one request');
  assert(remove.disabled === true, 'pending delete disables Remove draft');
  const pendingLabel = remove.textContent;
  renderCommentPopover();
  assert(remove.disabled === true, 'refresh keeps pending delete disabled');
  assert(remove.textContent === pendingLabel, 'refresh keeps the pending label');
  remove.listeners.click();
  assert(fetchCalls.length === 1, 'pending delete cannot be repeated');
  pendingResolve({ ok: true, status: 200, json: () => Promise.resolve({}) });
  await tick();
  console.log('floating comment panel refresh stability ok');
})().catch(function (e) { console.error(e && e.message ? e.message : e); process.exit(1); });
`

// TestProxyFloatingCommentPanelHardeningWhenNodeAvailable locks in two
// resilience fixes: rebuilding after the badge detached must remove the stale
// parent, and a successful DELETE must close the popover before the follow-up
// refresh so a failing refresh cannot leave the deleted draft open.
func TestProxyFloatingCommentPanelHardeningWhenNodeAvailable(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := reloadScript(12, 8765)
	start := strings.Index(script, "function locateComment(c){")
	end := strings.Index(script, "function renderCommentState(){")
	if start < 0 || end < start {
		t.Fatal("could not extract floating comment panel functions")
	}
	harness := popoverHarnessPrelude + script[start:end] + popoverHardeningChecks
	file := t.TempDir() + "/popover-hardening.js"
	if err := os.WriteFile(file, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("floating comment panel hardening: %v\n%s", err, output)
	}
}

const popoverHardeningChecks = `
function fetch(url, opts) {
  return Promise.resolve({ ok: true, status: 204, json: function () { return Promise.resolve({}); } });
}
function tick() { return new Promise(resolve => setTimeout(resolve, 0)); }
let refreshCalls = 0;
let refreshSawClosed = null;
refreshComments = function () {
  refreshCalls++;
  refreshSawClosed = commentPopover.hidden === true && popoverCommentId === null;
};
(async function () {
  // Rebuilding after the badge detaches must remove the stale parent so the
  // document keeps exactly one __gust_comment_popover node.
  commentState = [{ id: 'stale', path: '/', text: 'stale', state: 'created' }];
  pinOverlay = new El('div');
  pinOverlay.appendChild(pinFor('stale'));
  openCommentPopover('stale');
  const stale = commentPopover;
  assert(document.documentElement.children.indexOf(stale) >= 0, 'popover attaches to the document');
  popoverParts.badge.isConnected = false;
  const rebuilt = commentPopoverEl();
  assert(rebuilt !== stale, 'detached badge rebuilds the popover');
  assert(stale.removed === true && stale.isConnected === false, 'rebuild detaches the stale parent');
  assert(document.documentElement.children.indexOf(stale) === -1, 'stale parent is removed from the document');
  assert(document.documentElement.children.length === 1, 'only one popover stays attached');
  assert(rebuilt.id === '__gust_comment_popover', 'rebuilt popover keeps its id');

  // A successful DELETE must close the popover before the follow-up refresh
  // runs, so a failing refresh cannot leave the deleted draft open.
  commentState = [{ id: 'gone', path: '/', text: 'delete me', state: 'created' }];
  pinOverlay = new El('div');
  pinOverlay.appendChild(pinFor('gone'));
  openCommentPopover('gone');
  const remove = commentPopover.children[5].children[2];
  remove.listeners.click();
  await tick();
  assert(commentPopover.hidden === true, 'successful delete hides the popover');
  assert(popoverCommentId === null, 'successful delete clears the selected comment');
  assert(refreshCalls === 1, 'successful delete still triggers a follow-up refresh');
  assert(refreshSawClosed === true, 'popover is already closed when the follow-up refresh runs');
  console.log('floating comment panel hardening ok');
})().catch(function (e) { console.error(e && e.message ? e.message : e); process.exit(1); });
`

// TestProxyFloatingCommentPanelStaleCallbacksWhenNodeAvailable locks in the
// stale-callback guards: an in-flight reply or resolve must not clear another
// thread's draft, render an error into it, or close it when the user has moved
// on before the response arrives.
func TestProxyFloatingCommentPanelStaleCallbacksWhenNodeAvailable(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := reloadScript(12, 8765)
	start := strings.Index(script, "function locateComment(c){")
	end := strings.Index(script, "function renderCommentState(){")
	if start < 0 || end < start {
		t.Fatal("could not extract floating comment panel functions")
	}
	harness := popoverHarnessPrelude + script[start:end] + popoverStaleCallbackChecks
	file := t.TempDir() + "/popover-stale.js"
	if err := os.WriteFile(file, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("floating comment panel stale callbacks: %v\n%s", err, output)
	}
}

const popoverStaleCallbackChecks = `
const pending = [];
function fetch(url, opts) {
  return new Promise(function (resolve, reject) { pending.push({ url: url, opts: opts, resolve: resolve, reject: reject }); });
}
function tick() { return new Promise(function (resolve) { setTimeout(resolve, 0); }); }
(async function () {
  // Fix 1: text typed while the POST is in flight must survive a success.
  commentState = [{ id: 'a', path: '/', text: 'A', state: 'seen', messages: [] }];
  pinOverlay = new El('div');
  pinOverlay.appendChild(pinFor('a'));
  openCommentPopover('a');
  popoverReplyDraft = 'hello';
  renderCommentPopover();
  assert(popoverParts.reply.value === 'hello', 'reply textarea shows the draft');
  pending.length = 0;
  sendPopoverReply();
  assert(pending.length === 1, 'Send issues one reply POST');
  assert(pending[0].url.indexOf('/a/reply') >= 0, 'reply POST targets the open thread');
  popoverReplyDraft = 'hello world';
  popoverParts.reply.value = 'hello world';
  pending[0].resolve({ ok: true, status: 200, json: function () { return Promise.resolve({}); } });
  await tick();
  assert(popoverReplyDraft === 'hello world', 'success keeps text typed during the POST');
  assert(popoverParts.reply.value === 'hello world', 'textarea keeps text typed during the POST');
  assert(popoverReplyPending === false, 'success re-enables Send');

  // Fix 2: a stale reply success must not clear another thread's draft.
  commentState = [
    { id: 'a', path: '/', text: 'A', state: 'seen', messages: [] },
    { id: 'b', path: '/', text: 'B', state: 'seen', messages: [] }
  ];
  pinOverlay = new El('div');
  pinOverlay.appendChild(pinFor('a'));
  pinOverlay.appendChild(pinFor('b'));
  closeCommentPopover();
  openCommentPopover('a');
  popoverReplyDraft = 'to A';
  renderCommentPopover();
  pending.length = 0;
  sendPopoverReply();
  openCommentPopover('b');
  popoverReplyDraft = 'to B';
  renderCommentPopover();
  assert(popoverParts.reply.value === 'to B', 'thread B shows its own draft');
  pending[0].resolve({ ok: true, status: 200, json: function () { return Promise.resolve({}); } });
  await tick();
  assert(popoverCommentId === 'b', 'late reply success leaves thread B open');
  assert(popoverReplyDraft === 'to B', 'late reply success leaves thread B draft intact');
  assert(popoverParts.reply.value === 'to B', 'late reply success leaves thread B textarea intact');

  // Fix 2: a stale reply failure must not render into the new thread.
  closeCommentPopover();
  openCommentPopover('a');
  popoverReplyDraft = 'failing';
  renderCommentPopover();
  pending.length = 0;
  sendPopoverReply();
  openCommentPopover('b');
  popoverActionError = '';
  pending[0].reject(new Error('late reply failure'));
  await tick();
  assert(popoverCommentId === 'b', 'stale reply failure leaves thread B open');
  assert(popoverActionError === '', 'stale reply failure does not show on thread B');
  assert(commentPopover.hidden === false, 'stale reply failure does not close thread B');

  // Fix 2: a stale resolve must not close or error on another thread.
  closeCommentPopover();
  openCommentPopover('a');
  pending.length = 0;
  resolvePopoverThread();
  openCommentPopover('b');
  pending[0].resolve({ ok: true, status: 200, json: function () { return Promise.resolve({}); } });
  await tick();
  assert(popoverCommentId === 'b', 'stale resolve success leaves thread B open');
  assert(commentPopover.hidden === false, 'stale resolve success does not close thread B');

  // Fix 2: a stale resolve failure must not render into the new thread.
  closeCommentPopover();
  openCommentPopover('a');
  pending.length = 0;
  resolvePopoverThread();
  openCommentPopover('b');
  pending[0].reject(new Error('late resolve failure'));
  await tick();
  assert(popoverActionError === '', 'stale resolve failure does not show on thread B');
  assert(popoverCommentId === 'b', 'stale resolve failure leaves thread B open');

  console.log('floating comment panel stale-callback guards ok');
})().catch(function (e) { console.error(e && e.message ? e.message : e); process.exit(1); });
`

// TestProxyFloatingCommentPanelRefreshGenerationWhenNodeAvailable ensures a slow
// older GET cannot overwrite newer comment state after a reply or resolve.
func TestProxyFloatingCommentPanelRefreshGenerationWhenNodeAvailable(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := reloadScript(12, 8765)
	start := strings.Index(script, "function locateComment(c){")
	end := strings.Index(script, "function startCommentRefresh(){")
	if start < 0 || end < start {
		t.Fatal("could not extract floating comment panel functions")
	}
	harness := popoverHarnessPrelude + script[start:end] + popoverGenerationChecks
	file := t.TempDir() + "/popover-generation.js"
	if err := os.WriteFile(file, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("floating comment panel refresh generation: %v\n%s", err, output)
	}
}

const popoverGenerationChecks = `
const responses = [];
function fetch(url, opts) {
  return new Promise(function (resolve, reject) { responses.push({ url: url, opts: opts, resolve: resolve, reject: reject }); });
}
function tick() { return new Promise(function (resolve) { setTimeout(resolve, 0); }); }
commentUI = { querySelector: function (sel) { return sel === '[data-poll-error]' ? { textContent: '' } : null; } };
(async function () {
  commentState = [];
  refreshComments();
  refreshComments();
  assert(responses.length === 2, 'two comment GETs are in flight');
  // The newer response arrives first.
  responses[1].resolve({ ok: true, status: 200, json: function () { return Promise.resolve([{ id: 'new' }]); } });
  await tick();
  assert(commentState.length === 1 && commentState[0].id === 'new', 'newest response is applied');
  // A slower older response must not overwrite it.
  responses[0].resolve({ ok: true, status: 200, json: function () { return Promise.resolve([{ id: 'old' }]); } });
  await tick();
  assert(commentState.length === 1 && commentState[0].id === 'new', 'stale response is ignored');

  // A stale failure must not disturb state or surface an error either.
  commentState = [];
  refreshComments();
  refreshComments();
  responses[3].resolve({ ok: true, status: 200, json: function () { return Promise.resolve([{ id: 'fresh' }]); } });
  await tick();
  assert(commentState.length === 1 && commentState[0].id === 'fresh', 'fresh response is applied');
  responses[2].reject(new Error('old failure'));
  await tick();
  assert(commentState.length === 1 && commentState[0].id === 'fresh', 'stale failure leaves fresh state intact');
  console.log('floating comment panel refresh generation ordering ok');
})().catch(function (e) { console.error(e && e.message ? e.message : e); process.exit(1); });
`

func TestProxyForwardsWebSocketUpgrades(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			t.Fatalf("missing websocket upgrade header")
		}
		h, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("response writer cannot hijack")
		}
		conn, bufrw, err := h.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_, _ = bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = bufrw.Flush()
	}))
	defer app.Close()

	_, closeProxy := startProxyForTest(t, appPort(t, app.URL))
	defer closeProxy.Close()
	proxyAddr := strings.TrimPrefix(strings.TrimSuffix(closeProxy.URL, "/"), "http://")

	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET /ws HTTP/1.1\r\nHost: example\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	buf := make([]byte, 128)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buf[:n]), "101 Switching Protocols") {
		t.Fatalf("upgrade response = %q", buf[:n])
	}
}

func readBootMessage(t *testing.T, ctx context.Context, conn *websocket.Conn) string {
	t.Helper()
	msg := readBrowserMessage(t, ctx, conn)
	if msg.Type != "boot" || msg.BootID == "" {
		t.Fatalf("connect message = %+v, want proxy boot id", msg)
	}
	return msg.BootID
}

func readBrowserMessage(t *testing.T, ctx context.Context, conn *websocket.Conn) browserMessage {
	t.Helper()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var msg browserMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatal(err)
	}
	return msg
}

type proxyCloser struct {
	URL    string
	cancel context.CancelFunc
	*Server
}

func startProxyForTest(t *testing.T, appPort int) (string, *proxyCloser) {
	t.Helper()
	proxyPort := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	server, err := Start(ctx, config.Config{AppPort: appPort, ProxyPort: proxyPort, ProxyEnabled: true, CommentsEnabled: true}, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	closer := &proxyCloser{URL: fmt.Sprintf("http://127.0.0.1:%d", proxyPort), cancel: cancel, Server: server}
	return closer.URL, closer
}

func (c *proxyCloser) Close() {
	c.cancel()
	_ = c.Server.Close()
}

func TestBrowserCommentsDisabledByDefaultWhenConfigured(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	proxyURL, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()
	server.SetCommentsEnabled(false)

	for _, path := range []string{"/__gust/comments", "/__gust/comments/submit"} {
		req, err := http.NewRequest(http.MethodGet, proxyURL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status=%d, want 404", path, resp.StatusCode)
		}
	}
	injected := reloadScript(1, 8080, false)
	if !strings.Contains(injected, "if (false) { selecting=loadCommentMode(); createCommentUI(); startCommentRefresh(); }") {
		t.Fatal("disabled reload widget includes comment UI")
	}
}

func TestBrowserCommentsAPI(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	proxyURL, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()
	store, err := comments.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server.SetCommentStore(store)

	post := func(path, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, proxyURL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", proxyURL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := post("/__gust/comments", `{"path":"/one","text":"hello","html":"<div onclick=\"evil()\"><script>alert(1)</script><input value=\"secret\"><img src=\"data:image/png;base64,AAAA\"></div>","locator":"#target"}`)
	var created comments.Comment
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create status=%d: %s", resp.StatusCode, b)
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if created.State != comments.StateCreated || strings.Contains(created.HTML, "script") || strings.Contains(created.HTML, "onclick") || strings.Contains(created.HTML, "secret") || strings.Contains(created.HTML, "base64") {
		t.Fatalf("unsafe or unexpected created comment: %+v", created)
	}

	resp, err = http.Get(proxyURL + "/__gust/comments?path=%2Fone")
	if err != nil {
		t.Fatal(err)
	}
	var listed []comments.Comment
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("list = %+v", listed)
	}

	resp = post("/__gust/comments/submit", "{}")
	var batch comments.Batch
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("submit status=%d: %s", resp.StatusCode, b)
	}
	if err := json.NewDecoder(resp.Body).Decode(&batch); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(batch.Comments) != 1 || batch.Comments[0].State != comments.StateSubmitted {
		t.Fatalf("batch = %+v", batch)
	}
	claimed, err := store.NextBatch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Comments[0].State != comments.StateSeen {
		t.Fatalf("NextBatch state = %s", claimed.Comments[0].State)
	}
	current, err := store.Get(context.Background(), created.ID)
	if err != nil || current.State != comments.StateSeen {
		t.Fatalf("stored state=%s err=%v", current.State, err)
	}
}

// TestBrowserCommentThreadRoutes covers the human reply and resolve endpoints
// that turn a comment into a thread: text validation, same-origin enforcement,
// reply-while-review reopening, and terminal done behavior.
func TestBrowserCommentThreadRoutes(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	proxyURL, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()
	store, err := comments.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server.SetCommentStore(store)
	ctx := context.Background()
	created, err := store.Create(ctx, comments.Input{Path: "/", Text: "root", Locator: "body"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SubmitOne(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NextBatch(ctx); err != nil {
		t.Fatal(err)
	}

	post := func(path, body, origin string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, proxyURL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	decode := func(resp *http.Response) comments.Comment {
		t.Helper()
		defer resp.Body.Close()
		var c comments.Comment
		if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}

	if got := post("/__gust/comments/"+created.ID+"/reply", `{"text":"hi"}`, "http://attacker.invalid").StatusCode; got != 403 {
		t.Fatalf("cross-origin reply status=%d", got)
	}
	if got := post("/__gust/comments/"+created.ID+"/reply", `{"text":"  "}`, proxyURL).StatusCode; got != 400 {
		t.Fatalf("empty reply status=%d", got)
	}

	resp := post("/__gust/comments/"+created.ID+"/reply", `{"text":"human note"}`, proxyURL)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("reply status=%d: %s", resp.StatusCode, b)
	}
	replied := decode(resp)
	if len(replied.Messages) != 1 || replied.Messages[0].Author != comments.AuthorHuman || replied.Messages[0].Text != "human note" {
		t.Fatalf("reply messages = %+v", replied.Messages)
	}
	if replied.State != comments.StateSeen {
		t.Fatalf("reply state = %s", replied.State)
	}

	if _, err := store.Review(ctx, created.ID, "done, please check"); err != nil {
		t.Fatal(err)
	}
	resp = post("/__gust/comments/"+created.ID+"/reply", `{"text":"one more thing"}`, proxyURL)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("reopen status=%d: %s", resp.StatusCode, b)
	}
	reopened := decode(resp)
	if reopened.State != comments.StateSubmitted || len(reopened.Messages) != 3 {
		t.Fatalf("reopened = %+v", reopened)
	}

	resp = post("/__gust/comments/"+created.ID+"/resolve", `{}`, proxyURL)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("resolve status=%d: %s", resp.StatusCode, b)
	}
	if resolved := decode(resp); resolved.State != comments.StateDone {
		t.Fatalf("resolved state = %s", resolved.State)
	}

	if got := post("/__gust/comments/"+created.ID+"/reply", `{"text":"late"}`, proxyURL).StatusCode; got != 409 {
		t.Fatalf("done reply status=%d", got)
	}
	if got := post("/__gust/comments/00000000000000000000000000000000/reply", `{"text":"x"}`, proxyURL).StatusCode; got != 404 {
		t.Fatalf("unknown reply status=%d", got)
	}
	req, err := http.NewRequest(http.MethodGet, proxyURL+"/__gust/comments/"+created.ID+"/reply", nil)
	if err != nil {
		t.Fatal(err)
	}
	getResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	getResp.Body.Close()
	if getResp.StatusCode != 405 {
		t.Fatalf("GET reply status=%d", getResp.StatusCode)
	}
}

func TestBrowserAutosubmitOnlyTargetsSavedComment(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	proxyURL, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()
	store, err := comments.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server.SetCommentStore(store)
	other, err := store.Create(context.Background(), comments.Input{Path: "/", Text: "draft", Locator: "body"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Create(context.Background(), comments.Input{Path: "/", Text: "auto", Locator: "body"})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, proxyURL+"/__gust/comments/submit?id="+current.ID, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", proxyURL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var batch comments.Batch
	if err := json.NewDecoder(resp.Body).Decode(&batch); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || len(batch.Comments) != 1 || batch.Comments[0].ID != current.ID {
		t.Fatalf("autosubmit: status %d, batch %+v", resp.StatusCode, batch)
	}
	draft, err := store.Get(context.Background(), other.ID)
	if err != nil || draft.State != comments.StateCreated {
		t.Fatalf("unrelated draft: %+v, %v", draft, err)
	}
}

func TestBrowserDeleteDraft(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	proxyURL, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()
	store, err := comments.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server.SetCommentStore(store)
	draft, err := store.Create(context.Background(), comments.Input{Path: "/", Text: "draft", Locator: "body"})
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := store.Create(context.Background(), comments.Input{Path: "/", Text: "sent", Locator: "body"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SubmitOne(context.Background(), submitted.ID); err != nil {
		t.Fatal(err)
	}
	remove := func(id, origin string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodDelete, proxyURL+"/__gust/comments/"+id, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if got := remove(draft.ID, "http://attacker.invalid"); got != 403 {
		t.Fatalf("cross-origin delete: %d", got)
	}
	if got := remove(submitted.ID, proxyURL); got != 409 {
		t.Fatalf("submitted delete: %d", got)
	}
	if got := remove(draft.ID, proxyURL); got != 204 {
		t.Fatalf("draft delete: %d", got)
	}
	if got := remove(draft.ID, proxyURL); got != 404 {
		t.Fatalf("repeated delete: %d", got)
	}
}

func TestBrowserCommentsAPIRejectsInvalidRequests(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	proxyURL, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()
	store, err := comments.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server.SetCommentStore(store)
	cases := []struct {
		name, path, body string
		origin           string
		want             int
	}{
		{"invalid json", "/__gust/comments", `{`, "", 400},
		{"invalid path", "/__gust/comments", `{"path":"https://bad","text":"x","locator":"x"}`, "", 400},
		{"empty text", "/__gust/comments", `{"path":"/","text":" ","locator":"x"}`, "", 400},
		{"control char text", "/__gust/comments", `{"path":"/","text":"x\u0007y","locator":"x"}`, "", 400},
		{"text limit", "/__gust/comments", `{"path":"/","text":"` + strings.Repeat("x", 8193) + `","locator":"x"}`, "", 400},
		{"html limit", "/__gust/comments", `{"path":"/","text":"x","html":"` + strings.Repeat("x", 32769) + `","locator":"x"}`, "", 400},
		{"locator limit", "/__gust/comments", `{"path":"/","text":"x","locator":"` + strings.Repeat("x", 8193) + `"}`, "", 400},
		{"unknown field", "/__gust/comments", `{"path":"/","text":"x","locator":"x","state":"done"}`, "", 400},
		{"origin mismatch", "/__gust/comments", `{"path":"/","text":"x","locator":"x"}`, "http://attacker.invalid", 403},
		{"no created", "/__gust/comments/submit", `{}`, "", 409},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, proxyURL+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				b, _ := io.ReadAll(resp.Body)
				t.Fatalf("status=%d body=%s", resp.StatusCode, b)
			}
			var payload map[string]map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["error"]["code"] == "" {
				t.Fatalf("missing error code: %+v", payload)
			}
		})
	}
	resp, err := postWithHeaders(proxyURL+"/__gust/comments", strings.Repeat(" ", maxCommentBody+1)+`{}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 413 {
		t.Fatalf("oversized body status=%d", resp.StatusCode)
	}

	resp, err = http.Get(proxyURL + "/__gust/comments/extra")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("unknown reserved path status=%d", resp.StatusCode)
	}
}

func TestBrowserCommentsAPIAcceptsMultilineText(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	proxyURL, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()
	store, err := comments.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server.SetCommentStore(store)

	text := "First line.\nSecond line.\n\tIndented bullet."
	body, err := json.Marshal(map[string]string{"path": "/", "text": text, "html": "<p>x</p>", "locator": "#x"})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, proxyURL+"/__gust/comments", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", proxyURL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create status=%d: %s", resp.StatusCode, b)
	}
	var created comments.Comment
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Text != text {
		t.Fatalf("text = %q, want %q", created.Text, text)
	}
}

func postWithHeaders(target, body string, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return http.DefaultClient.Do(req)
}

func TestProxyDisabledHasNoServer(t *testing.T) {
	server, err := Start(context.Background(), config.Config{ProxyEnabled: false}, nil)
	if err != nil || server != nil {
		t.Fatalf("Start disabled = (%v, %v)", server, err)
	}
}

func appPort(t *testing.T, rawURL string) int {
	t.Helper()
	u := strings.TrimPrefix(rawURL, "http://127.0.0.1:")
	var port int
	if _, err := fmt.Sscanf(u, "%d", &port); err != nil {
		t.Fatalf("parse port from %q: %v", rawURL, err)
	}
	return port
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
