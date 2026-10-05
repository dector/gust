package proxy

import (
	"os/exec"
	"strings"
	"testing"
)

func TestCommentDraftPreference(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := reloadScript(1, 8765)
	start := strings.Index(script, `const draftLabel=document.createElement("label");`)
	end := strings.Index(script, `const save=document.createElement("button");save.type="button";save.dataset.save="";save.textContent="Submit";`)
	if start < 0 || end < start {
		t.Fatal("missing draft checkbox before Submit")
	}
	harness := `const document={createElement(){return {dataset:{},checked:false,addEventListener(event,fn){this.change=fn},append(){}}},createTextNode(text){return text}};
 const localStorage={value:null,getItem(){return this.value},setItem(key,value){if(key!=="__gust_comment_as_draft")throw Error("wrong key");this.value=value}};
 function mount(){` + script[start:end] + `return asDraft;}
 let box=mount();if(box.checked)throw Error("must default to submit");box.checked=true;box.change();if(!mount().checked)throw Error("draft preference not restored");box.checked=false;box.change();if(mount().checked)throw Error("submit preference not restored");
 localStorage.getItem=()=>{throw Error("storage unavailable")};localStorage.setItem=()=>{throw Error("storage unavailable")};box=mount();box.checked=true;box.change();
 let batchMode=false,auto={checked:true},asDraft={checked:false},wantsSendNow=false;
 function shouldSubmit(){return !batchMode&&auto.checked&&!asDraft.checked&&!wantsSendNow;}
 if(!shouldSubmit())throw Error("normal comment must submit");asDraft.checked=true;if(shouldSubmit())throw Error("draft must not submit");`
	if !strings.Contains(script, `const shouldAutoSubmit=!batchMode&&auto.checked&&!asDraft.checked&&!wantsSendNow;`) {
		t.Fatal("draft must suppress autosubmit")
	}
	if output, err := exec.Command(node, "-e", harness).CombinedOutput(); err != nil {
		t.Fatalf("draft preference: %v\n%s", err, output)
	}
}
