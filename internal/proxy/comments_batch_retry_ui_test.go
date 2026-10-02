package proxy

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestBatchEditorRetryActionsWhenNodeAvailable(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := reloadScript(1, 8765, true)
	ui := strings.Index(script, "function createCommentUI(){")
	if ui < 0 {
		t.Fatal("comment editor missing")
	}
	start := strings.Index(script[ui:], `  const close=document.createElement("button");`)
	end := strings.Index(script[ui:], `  const title=document.createElement("strong");`)
	if start < 0 || end <= start {
		t.Fatal("could not extract editor actions")
	}
	code := script[ui+start : ui+end]
	harness := `
const assert = require('node:assert/strict');
class Element {
  constructor() { this.dataset={}; this.handlers={}; this.value=''; this.disabled=false; this.readOnly=false; this.textContent=''; }
  addEventListener(type, handler) { this.handlers[type]=handler; }
  setAttribute() {}
  append() {}
  click() { if (!this.disabled && this.handlers.click) this.handlers.click({}); }
}
let nextID=0;
const settle=()=>new Promise(resolve=>setImmediate(resolve));
function setup(createResponse, submitResponse=()=>Promise.resolve()) {
  const document={createElement:()=>new Element(),createTextNode:text=>({textContent:text})};
  const editor=new Element(),textarea=new Element();
  const selectedElement={},selectedPoint=null,reattachId=null,batchMode=true;
  const location={pathname:'/fixture'},auto={checked:true},submitResult=new Element();
  const requests=[],submitted=[];
  let finished=0;
  function newCommentRequestID(){return (++nextID).toString(16).padStart(32,'0');}
  function locatorFor(){return '#target';}
  function safeOuterHTML(){return '<button id="target">Target</button>';}
  function refreshComments(){}
  function finishCommentEdit(){finished++;}
  function closeCommentMode(){finished++;}
  function submitCreatedComment(id){submitted.push(id);return submitResponse(id);}
  function fetch(url,options){
    assert.equal(url,'/__gust/comments');
    const body=JSON.parse(options.body);requests.push(body);
    return createResponse(body,requests.length);
  }
` + code + `
  return {editor,textarea,save,saveDraft,sendNow,close,result,requests,submitted,get finished(){return finished;}};
}
function response(status,body){return Promise.resolve({ok:status>=200&&status<300,status,json:()=>Promise.resolve(body)});}
function created(body){return {id:body.id,state:'created',inBatch:body.inBatch};}
function ctrlEnter(ui){ui.textarea.handlers.keydown({key:'Enter',ctrlKey:true,isComposing:false,preventDefault(){}});}
(async()=>{
  // A definitive rejection must permit fixing text and leaving the editor.
  const invalid=setup((body,n)=>n===1?response(400,{error:{message:'invalid text'}}):response(201,created(body)));
  invalid.textarea.value='invalid';invalid.saveDraft.click();await settle();
  assert.equal(invalid.textarea.readOnly,false);
  assert.equal(invalid.editor.dataset.pendingCreate,'');
  assert.match(invalid.result.textContent,/Could not save: invalid text/);
  invalid.close.click();assert.equal(invalid.finished,1);
  invalid.textarea.value='corrected';ctrlEnter(invalid);await settle();
  assert.equal(invalid.finished,2);assert.equal(invalid.submitted.length,0);
  assert.notEqual(invalid.requests[0].id,invalid.requests[1].id);

  // The first create committed, but its response was lost. Ctrl+Enter must
  // retry the same immutable payload and SAVE, not inherit Send now intent.
  const stored=new Map();
  const lost=setup((body,n)=>{
    if(!stored.has(body.id))stored.set(body.id,created(body));
    return n===1?Promise.reject(new Error('lost response')):response(200,stored.get(body.id));
  });
  lost.textarea.value='one-shot idea';lost.sendNow.click();await settle();
  assert.equal(lost.textarea.readOnly,true);
  assert.equal(lost.editor.dataset.pendingCreate,'1');
  ctrlEnter(lost);await settle();
  assert.equal(stored.size,1);assert.deepEqual(lost.requests[0],lost.requests[1]);
  assert.equal(lost.submitted.length,0);assert.equal(lost.finished,1);
  assert.equal(lost.requests[0].inBatch,false);assert.equal(lost.textarea.readOnly,false);

  // A saved draft cannot silently accept edits that would never be persisted.
  const failedSend=setup(body=>response(201,created(body)),()=>Promise.reject(new Error('send unavailable')));
  failedSend.textarea.value='saved draft';failedSend.sendNow.click();await settle();
  assert.equal(failedSend.textarea.readOnly,true);
  assert.equal(failedSend.editor.dataset.sendPending,'1');
  ctrlEnter(failedSend);await settle();
  assert.equal(failedSend.requests.length,1);assert.equal(failedSend.submitted.length,1);
  assert.equal(failedSend.finished,1);assert.equal(failedSend.editor.dataset.sendPending,'');

  // A clicked action while a save is pending must not leak Send now intent
  // into a later keyboard save after the definitive rejection.
  let rejectPending;
  const busy=setup((body,n)=>n===1?new Promise(resolve=>{rejectPending=()=>resolve({ok:false,status:400,json:()=>Promise.resolve({error:{message:'rejected'}})});}):response(201,created(body)));
  busy.textarea.value='draft';busy.saveDraft.click();busy.sendNow.click();
  rejectPending();await settle();ctrlEnter(busy);await settle();
  assert.equal(busy.submitted.length,0);assert.equal(busy.finished,1);

  // Malformed success responses are ambiguous: retain the key for safe retry.
  const malformed=setup((body,n)=>n===1?response(201,{}):response(200,created(body)));
  malformed.textarea.value='recoverable';malformed.saveDraft.click();await settle();
  assert.equal(malformed.textarea.readOnly,true);
  ctrlEnter(malformed);await settle();
  assert.equal(malformed.requests[0].id,malformed.requests[1].id);
  assert.equal(malformed.finished,1);assert.equal(malformed.submitted.length,0);
  console.log('batch editor retry actions ok');
})().catch(error=>{console.error(error);process.exitCode=1;});
`
	file := t.TempDir() + "/batch-editor-retry.js"
	if err := os.WriteFile(file, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("editor retry actions: %v\n%s", err, output)
	}
}
