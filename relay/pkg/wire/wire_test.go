package wire

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestFrames(t *testing.T) {
	var buf bytes.Buffer
	WriteFrame(&buf, FrameData, []byte("hello"))
	WriteFrame(&buf, FrameSize, SizePayload(120, 40))
	code := 3
	WriteFrame(&buf, FrameExit, ExitPayload(&Exit{Code: &code}))
	if got := buf.Bytes()[:10]; !bytes.Equal(got, []byte{0, 0, 0, 0, 5, 'h', 'e', 'l', 'l', 'o'}) {
		t.Fatalf("% x", got)
	}
	typ, p, err := ReadFrame(&buf, nil)
	if err != nil || typ != FrameData || string(p) != "hello" {
		t.Fatal(typ, p, err)
	}
	typ, p, _ = ReadFrame(&buf, nil)
	if c, r, ok := ParseSize(p); typ != FrameSize || !ok || c != 120 || r != 40 {
		t.Fatal(typ, c, r)
	}
	typ, p, _ = ReadFrame(&buf, nil)
	var e Exit
	if typ != FrameExit || json.Unmarshal(p, &e) != nil || *e.Code != 3 || string(p) != `{"code":3}` {
		t.Fatalf("%d %s", typ, p)
	}
	signaled, _ := json.Marshal(Exit{Signal: "SIGHUP"})
	if string(signaled) != `{"code":null,"signal":"SIGHUP"}` {
		t.Fatal(string(signaled))
	}
}

func TestAgentJSON(t *testing.T) {
	data, _ := json.Marshal(Agent{ID: "L/a7f3k2", Machine: "L", Kind: KindClaude, State: StateApproval,
		Attention: &Attention{Kind: "approval", Title: "Bash", Detail: "git push", Options: []string{Allow, Always, Deny}}})
	for _, want := range []string{`"id":"L/a7f3k2"`, `"stateSince":`, `"attention":{"kind":"approval","title":"Bash","detail":"git push","options":["allow","always","deny"]}`, `"exit":null`, `"size":{"cols":0,"rows":0}`} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("no %s in %s", want, data)
		}
	}
	rpc := (&RPCError{Code: RPCServer, Message: "no agent", Data: &ErrorData{Code: CodeNotFound}}).Err()
	if rpc.Code != CodeNotFound || rpc.Error() != "no agent" {
		t.Fatal(rpc)
	}
}
