package proxy

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestStandaloneCommentsPanelWhenNodeAvailable(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := reloadScript(1, 8765, true, true)
	extract := func(start, end string) string {
		t.Helper()
		a, b := strings.Index(script, start), strings.Index(script, end)
		if a < 0 || b <= a {
			t.Fatalf("could not extract %s", start)
		}
		return script[a:b]
	}
	for _, stale := range []string{
		`widget.appendChild(commentUI)`,
		`#__gust_widget.__gust_open.__gust_commenting #__gust_comments`,
		`#__gust_widget.__gust_wide #__gust_comments`,
	} {
		if strings.Contains(script, stale) {
			t.Fatalf("comments panel still coupled to status widget: %s", stale)
		}
	}
	harness := `
function assert(ok, message) { if (!ok) throw Error(message); }
class Element {
  constructor(tag) { this.tagName=tag;this.children=[];this.dataset={};this.attrs={};this.listeners={};this.hidden=false;this.style={}; }
  append(...nodes) { nodes.forEach(n=>this.appendChild(n)); }
  appendChild(n) { if(n.parentNode)n.parentNode.children=n.parentNode.children.filter(c=>c!==n);this.children.push(n);n.parentNode=this;return n; }
  insertBefore(n, before) { this.appendChild(n); }
  setAttribute(k,v) { this.attrs[k]=v; }
  addEventListener(k,fn) { this.listeners[k]=fn; }
  focus() { document.activeElement=this; }
  querySelector(s) { const key=s.slice(1,-1).replace(/^data-/, '').replace(/-([a-z])/g,(_,c)=>c.toUpperCase());for(const c of this.children){if(key in c.dataset)return c;const nested=c.querySelector(s);if(nested)return nested;}return null; }
  querySelectorAll() { return []; }
}
const document={body:new Element('body'),documentElement:new Element('html'),createElement:t=>new Element(t),createTextNode:t=>new Element('text')};
let commentUI=null,commentBubble=null,commentUnreadBadge=null,commentPageCount=null,batchSendButton=null;
const commentToolbar=new Element('div'),windButton=new Element('button');commentToolbar.appendChild(windButton);
const commentAddIconSvg='',batchCursor='auto';
let selecting=false,batchMode=false,editorOpen=false,popoverCommentId=null,commentRefreshTimer=null;
let cancelCalls=0,uiUpdates=0,fetches=0,renders=0;
function cancelCommentMode(){cancelCalls++;return true;}
function updateCommentUI(){uiUpdates++;}
function updateCommentsPanelPosition(){}
function beginCommentTool(){throw Error('panel must not start selection');}
function openFirstUnreadComment(){}
function sendCommentBatch(){}
let gustInfo=null,gustProcess=null,reloadedAt=0;const gustAppPort=8765;
const gustPanel=new Element('div');gustPanel.innerHTML='';
function panelRow(){return '';}
function ago(){return '';}
function taskGroup(){return '';}
function renderCommentState(){renders++;}
` + extract(`function toggleCommentsPanel(open){`, `function updateHoverPath(`) + extract(`function renderPanel(){`, `function refreshInfo(){`) + `
createCommentUI();
const panel=commentUI,editor=document.documentElement.children.find(n=>n.id==='__gust_comment_editor');
assert(panel.parentNode===document.body,'panel mounts independently on body');
assert(editor && editor.parentNode===document.documentElement,'editor remains detached from list');
assert(commentToolbar.parentNode!==panel,'wind and sound toolbar stays out of comments panel');
assert(panel.hidden,'panel starts closed');
assert(panel.attrs.role==='region'&&panel.attrs['aria-label']==='Comments','panel has accessible name');
assert(commentPageCount.tagName==='button'&&commentPageCount.type==='button','counter is a native button');
assert(commentPageCount.attrs['aria-controls']===panel.id&&commentPageCount.attrs['aria-expanded']==='false','counter exposes controlled panel');
commentPageCount.hidden=false;
let prevented=0,stopped=0;
const event={preventDefault(){prevented++;},stopPropagation(){stopped++;}};
function clickCounter(){commentPageCount.listeners.click(event);}
clickCounter();
assert(!panel.hidden&&commentPageCount.attrs['aria-expanded']==='true','click opens panel');
assert(document.activeElement===panel.querySelector('[data-close-comments]'),'open focuses close button');
assert(!selecting&&!editorOpen&&!batchMode,'opening does not enter comment mode');
selecting=true;batchMode=true;const updates=uiUpdates;
clickCounter();
assert(panel.hidden&&commentPageCount.attrs['aria-expanded']==='false','second click closes panel');
assert(selecting&&batchMode&&uiUpdates===updates,'closing preserves comment mode');
assert(document.activeElement===commentPageCount,'close restores counter focus');
clickCounter();panel.querySelector('[data-close-comments]').listeners.click();
assert(panel.hidden&&selecting&&batchMode,'close button hides only the panel');
clickCounter();assert(handleCommentEscape(),'Escape handled');
assert(panel.hidden&&cancelCalls===0&&selecting&&batchMode,'Escape closes panel without exiting selection');
assert(handleCommentEscape()&&cancelCalls===1,'Escape falls back to existing comment cancellation when panel closed');
clickCounter();editorOpen=true;
handleCommentEscape();assert(!panel.hidden&&cancelCalls===2,'open editor retains existing Escape priority');
editorOpen=false;popoverCommentId='thread';handleCommentEscape();
assert(!panel.hidden&&cancelCalls===3,'thread popover retains existing Escape priority');
popoverCommentId=null;
renderPanel();
assert(!panel.hidden&&commentToolbar.parentNode===gustPanel,'status refresh keeps toolbar in main panel without changing comments visibility');
assert(windButton.parentNode===commentToolbar,'wind toggle remains in main toolbar');
assert(renders===0&&uiUpdates===updates,'status refresh does not rerender independent comments');
assert(prevented===stopped&&prevented>0,'counter clicks prevent selection propagation');
` + extract(`function refreshComments(){`, `function updateEditorPosition(){`) + `
let commentFetchGeneration=0,commentState=[];
function fetch(){fetches++;return Promise.resolve({ok:true,json(){return Promise.resolve([{id:'one',state:'created'}]);}});}
function setInterval(fn,ms){assert(ms===3000,'polling interval preserved');return 123;}
panel.hidden=true;startCommentRefresh();startCommentRefresh();
setTimeout(()=>{
 assert(fetches===1&&commentRefreshTimer===123,'hidden panel continues polling with only one timer');
 assert(commentState.length===1&&renders===1,'polling updates comments while panel closed');
 assert(panel.hidden,'polling cannot open panel');
},0);
`
	file := t.TempDir() + "/comments-panel.js"
	if err := os.WriteFile(file, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("standalone comments panel: %v\n%s", err, output)
	}
}
