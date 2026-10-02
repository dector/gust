package proxy

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCommentsPanelCollisionPositionWhenNodeAvailable(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := reloadScript(1, 8765)
	extract := func(start, end string) string {
		t.Helper()
		a, b := strings.Index(script, start), strings.Index(script, end)
		if a < 0 || b <= a {
			t.Fatalf("could not extract %s", start)
		}
		return script[a:b]
	}
	harness := `
const assert=require('node:assert/strict');
function rect(left,top,width,height){return {left,top,right:left+width,bottom:top+height,width,height};}
let innerWidth=1200,innerHeight=900;
let panelWidth=420,panelHeight=300;
const commentUI={hidden:false,style:{},querySelector(){return {focus(){}};},getBoundingClientRect(){return rect(this.style.left==='16px'?16:innerWidth-16-panelWidth,innerHeight-122-panelHeight,panelWidth,panelHeight);}};
let editorOpen=false,editorAnchor=null,selectedElement=null,selectedPoint=null;
const editor={hidden:true,style:{},offsetWidth:320,offsetHeight:160,getBoundingClientRect(){return rect(parseFloat(this.style.left),parseFloat(this.style.top),this.offsetWidth,this.offsetHeight);}};
const document={querySelector(){return editor;}};
let commentPopover=null,popoverCommentId=null,popoverAnchor=null,pinOverlay=null;
let popoverRenderKey='',popoverDeletePending=false,popoverActionError='',popoverReplyPending=false,popoverResolvePending=false,popoverDeleteToken=0;
const commentPageCount={hidden:false,setAttribute(){},focus(){}};
function flushPendingRefresh(){}
let observerCallback,observed=[];
class ResizeObserver { constructor(cb){observerCallback=cb;} observe(el){observed.push(el);} }
let connectors=[];
function updateCommentConnector(dialog,point){connectors.push({dialog,point});}
` + extract("function commentsPanelSide(", "// Keep the connector inside its dialog:") + extract("function positionCommentPopover(){", "function removePopoverDraft(") + extract("function updateEditorPosition(){", "function currentTargetEl(){") + extract("function toggleCommentsPanel(open){", "function handleCommentEscape(){") + `
const rightDialog=rect(900,550,280,160);
assert.equal(commentsPanelSide(420,300,1200,900,[]),'right');
assert.equal(commentsPanelSide(420,300,1200,900,[rect(900,20,280,160)]),'right','no vertical overlap');
assert.equal(commentsPanelSide(420,300,1200,900,[rect(440,550,300,160)]),'right','no horizontal overlap');
assert.equal(commentsPanelSide(420,300,1200,900,[rightDialog]),'left','viable opposite side');
assert.equal(commentsPanelSide(420,300,700,900,[rect(300,550,380,160)]),'right','left also overlaps on a narrow screen');
assert.equal(commentsPanelSide(420,300,430,900,[rect(200,550,220,160)]),'right','left candidate exceeds viewport');
assert.equal(commentsPanelSide(420,300,1200,420,[rect(900,0,280,280)]),'right','panel cannot fit vertically');
assert.equal(commentsPanelSide(420,300,1200,900,[rightDialog,rect(0,550,300,160)]),'right','both sides obstructed');
assert.equal(commentsPanelSide(420,300,1200,900,[rect(1184,550,16,160)]),'right','touching edge is not overlap');
assert.equal(commentsPanelSide(420,300,1200,900,[rightDialog,rect(436,550,300,160)]),'left','exact left edge clearance fits');
assert.equal(commentsPanelSide(420,300,1200,900,[rightDialog,rect(435,550,300,160)]),'right','one pixel overlap does not fit');

updateCommentsPanelPosition();assert.equal(commentUI.style.left,'');
commentPopover={hidden:false,style:{},offsetWidth:300,offsetHeight:160,getBoundingClientRect(){return rect(parseFloat(this.style.left),parseFloat(this.style.top),this.offsetWidth,this.offsetHeight);}};
popoverCommentId='one';popoverAnchor=rect(850,550,20,20);
positionCommentPopover();
assert.equal(commentUI.style.left,'16px');assert.equal(commentUI.style.right,'auto');
assert.equal(commentPopover.style.left,'882px');assert.equal(commentPopover.style.top,'550px');
const anchor=popoverAnchor,connector=connectors.at(-1);
for(let i=0;i<5;i++)updateCommentsPanelPosition();
assert.equal(commentUI.style.left,'16px','repeated updates do not oscillate');
assert.equal(popoverAnchor,anchor);assert.equal(connectors.at(-1),connector,'panel moves do not alter dialog or connector');
assert.ok(observed.includes(commentUI)&&observed.includes(commentPopover));
assert.equal(new Set(observed).size,observed.length,'surfaces only observed once');

commentPopover.offsetHeight=400;observerCallback();
assert.equal(commentUI.style.left,'16px','content resize recomputes placement');
assert.equal(commentPopover.style.top,'488px','existing dialog clamping is preserved');
closeCommentPopover();assert.equal(commentUI.style.left,'');assert.equal(commentUI.style.right,'','closing thread restores CSS default');

editorOpen=true;editor.hidden=false;editorAnchor={x:400,y:550};
updateEditorPosition();
assert.equal(editor.style.left,'412px');assert.equal(editor.style.top,'562px');
// The editor initially opens beside this point, with no overlap.
assert.equal(commentUI.style.left,'','default retained when anchored editor is clear');
editorAnchor={x:1100,y:550};updateEditorPosition();
assert.equal(commentUI.style.left,'16px');assert.equal(editor.style.left,'768px');
assert.deepEqual(connectors.at(-1),{dialog:editor,point:null});
innerWidth=700;updateEditorPosition();assert.equal(commentUI.style.left,'','no viable opposite side after viewport resize');
innerWidth=1200;updateEditorPosition();assert.equal(commentUI.style.left,'16px');
editorOpen=false;editor.hidden=true;updateCommentsPanelPosition();assert.equal(commentUI.style.left,'','editor closure restores default');
editorOpen=true;editor.hidden=false;updateEditorPosition();
toggleCommentsPanel(false);assert.ok(commentUI.hidden);
editorOpen=false;editor.hidden=true;toggleCommentsPanel(true);assert.equal(commentUI.style.left,'','reopening after dialog closure restores default');
`
	// The lifecycle functions must call the same helper after content and visibility updates.
	for _, check := range []string{
		"updateCommentsPanelPosition();\n  renderCommentPopover();",
		"updateCommentsPanelPosition();\n  scheduleRenderPath();",
	} {
		if !strings.Contains(script, check) {
			t.Errorf("missing panel lifecycle wiring %q", check)
		}
	}
	file := t.TempDir() + "/panel-position.js"
	if err := os.WriteFile(file, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("comments panel collision positioning: %v\n%s", err, output)
	}
}
