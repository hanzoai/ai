package controllers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/ai/model"
)

func finishFrames(fin string, mid ...string) []string {
	f := []string{chunkFrame(`{"role":"assistant","content":""}`, ""),
		chunkFrame(`{"content":"Just "}`, ""), chunkFrame(`{"content":"an answer."}`, "")}
	for _, m := range mid {
		f = append(f, chunkFrame(m, ""))
	}
	return append(f, chunkFrame(`{}`, fin))
}

// A flagged route's plain answer ends in any spelling of a finished turn: it is
// delivered whole, streamed and buffered. length and content_filter are cuts.
func TestFinishSpellingsDeliverThePlainAnswer(t *testing.T) {
	for _, fin := range []string{"stop", "STOP", "eos", "end_turn", "End_Turn"} {
		w := eatenWorld(t, finishFrames(fin)...)
		h := liftHear(t, sent(w.chat(`{"model":"`+thinkSku+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
		if h.text != "Just an answer." {
			t.Errorf("stream fin=%q: %q", fin, h.text)
		}
		w = eatenWorld(t, finishFrames(fin)...)
		if a := anthropicText(t, w); a != "Just an answer." {
			t.Errorf("messages fin=%q: %q", fin, a)
		}
		body := func(w http.ResponseWriter, _ int) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"u","object":"chat.completion","created":1,"model":"a-up","choices":[{"index":0,"message":{"role":"assistant","content":"Just an answer."},"finish_reason":"` + fin + `"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
		}
		bw := newRelayWorld(t, body, never(t))
		thinkRoute(t)
		eatenRoute(t)
		if !strings.Contains(sent(bw.chat(`{"model":"`+thinkSku+`","messages":[{"role":"user","content":"hi"}]}`)), "Just an answer.") {
			t.Errorf("buffered fin=%q lost the answer", fin)
		}
	}
	for _, fin := range []string{"length", "content_filter"} {
		w := eatenWorld(t, finishFrames(fin)...)
		h := liftHear(t, sent(w.chat(`{"model":"`+thinkSku+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
		if h.text != "" {
			t.Errorf("cut fin=%q released %q", fin, h.text)
		}
	}
}

// An open think block at a tool-call finish is thinking, never text.
func TestOpenThinkAtToolCallsIsDropped(t *testing.T) {
	call := `{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"exec_command","arguments":"{}"}}]}`
	for _, fin := range []string{"tool_calls", "function_call"} {
		fr := []string{chunkFrame(`{"role":"assistant","content":""}`, ""),
			chunkFrame(`{"content":"<think>secret plan"}`, ""), chunkFrame(call, ""), chunkFrame(`{}`, fin)}
		for name, mk := range map[string]func() *relayWorld{
			"unflagged": func() *relayWorld { return thinkWorld(t, fr...) },
			"eaten":     func() *relayWorld { return eatenWorld(t, fr...) },
		} {
			h := liftHear(t, sent(mk().chat(`{"model":"`+thinkSku+`","stream":true,`+relayTool+`,"messages":[{"role":"user","content":"hi"}]}`)))
			if strings.Contains(h.text, "secret") {
				t.Errorf("%s %s leaked %q", name, fin, h.text)
			}
		}
	}
}

func TestFinishedAndCuts(t *testing.T) {
	for r, want := range map[string]bool{"stop": true, "EOS": true, "end_turn": true, "tool_calls": true, "function_call": true, "length": false, "content_filter": false, "": false} {
		if model.Finished(r) != want {
			t.Errorf("Finished(%q) != %v", r, want)
		}
	}
	if !model.Cuts("tool_calls") || !model.Cuts("length") || model.Cuts("stop") || model.Cuts("end_turn") {
		t.Error("Cuts")
	}
	if got := model.StripReasoning("<think>plan", false, model.Cuts("function_call")); got != "" {
		t.Errorf("open block at function_call: %q", got)
	}
}
