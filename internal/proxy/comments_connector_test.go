package proxy

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCommentDialogConnectorsWhenNodeAvailable(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := reloadScript(12, 8765)
	extract := func(start, end string) string {
		a, b := strings.Index(script, start), strings.Index(script, end)
		if a < 0 || b <= a {
			t.Fatalf("could not extract %s", start)
		}
		return script[a:b]
	}
	harness := popoverHarnessPrelude + "\nfunction updateCommentsPanelPosition() {}\n" + extract("function updateCommentConnector(", "function removePopoverDraft(") + extract("function updateEditorPosition(){", "function currentTargetEl(){") + `
Object.assign(assert, require('node:assert/strict'));
const pop = new El('div'); commentPopover=pop; popoverCommentId='one';
pop.getBoundingClientRect=()=>({left:parseFloat(pop.style.left),right:parseFloat(pop.style.left)+300,top:parseFloat(pop.style.top),bottom:parseFloat(pop.style.top)+160});
const pin=new El('button');pin.dataset.commentId='one';pin.style.display='';
let pinX=400,pinY=200;
pin.getBoundingClientRect=()=>({left:pinX-10,right:pinX+10,top:pinY-10,bottom:pinY+10,width:20,height:20});
pinOverlay=new El('div');pinOverlay.appendChild(pin);
positionCommentPopover();
const svg=pop.__gustConnector,line=svg.firstChild;
assert.equal(svg.parentNode,pop); // lifecycle follows the dialog
assert.equal(svg.attrs['aria-hidden'],'true');
assert.match(svg.style.cssText,/pointer-events:none/);
assert.equal(line.attrs.x2,400);assert.equal(line.attrs.y2,200);
assert.equal(line.attrs.x1,422);assert.equal(line.attrs.y1,200);
pinX=950;pinY=780;positionCommentPopover();
assert.equal(pop.__gustConnector,svg);assert.equal(line.attrs.x2,950);
assert.equal(line.attrs.x1,928);assert.equal(line.attrs.y1,780); // within dialog's clamped vertical span
pin.style.display='none';positionCommentPopover();assert.equal(svg.style.display,'none');
closeCommentPopover();assert.equal(pop.hidden,true);
commentUI={};let editorOpen=true,editorAnchor={x:100,y:100};
let rect={left:100,top:100,width:200,height:80};
let selectedElement={isConnected:true,getBoundingClientRect:()=>rect},selectedPoint={x:.25,y:.5};
const editor=new El('div');
document.querySelector=()=>editor;
editor.getBoundingClientRect=()=>({left:parseFloat(editor.style.left),right:parseFloat(editor.style.left)+300,top:parseFloat(editor.style.top),bottom:parseFloat(editor.style.top)+160});
updateEditorPosition();
const editorSvg=editor.__gustConnector;
assert.equal(editorSvg.firstChild.attrs.x2,150);assert.equal(editorSvg.firstChild.attrs.y2,140);
rect.top=50;rect.left=200;updateEditorPosition(); // nested/document scroll
assert.equal(editor.__gustConnector,editorSvg);
assert.equal(editorSvg.firstChild.attrs.x2,250);assert.equal(editorSvg.firstChild.attrs.y2,90);
rect.top=-200;updateEditorPosition();assert.equal(editorSvg.style.display,'none');
selectedElement.isConnected=false;updateEditorPosition();assert.equal(editorSvg.style.display,'none');
updateCommentConnector(editor,{x:parseFloat(editor.style.left)+10,y:parseFloat(editor.style.top)+10});
assert.equal(editorSvg.style.display,'none'); // anchor occluded by clamped dialog
`
	file := t.TempDir() + "/connectors.js"
	if err := os.WriteFile(file, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("comment connector behavior: %v\n%s", err, output)
	}
	for _, check := range []string{
		`window.addEventListener("scroll",function(){if(editorOpen)updateEditorPosition();},true);`,
		`window.addEventListener("resize",function(){if(editorOpen)updateEditorPosition();scheduleRenderPath();});`,
		"positionCommentPopover();\n}",
		`editor.hidden=!editorOpen;editor.style.display=editorOpen?"block":"none"`,
	} {
		if !strings.Contains(script, check) {
			t.Errorf("missing connector lifecycle wiring %q", check)
		}
	}
}
