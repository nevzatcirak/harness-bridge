package claudecode

// ADR-017 A2 S4: the lenient api_retry / no_response decoder and the tool_use
// id that pairs parallel tools with their results. Fabricated lines only.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeAPIRetryFields(t *testing.T) {
	ev := DecodeLine([]byte(`{"type":"system","subtype":"api_retry","session_id":"u","attempt":2,"max_retries":5,"retry_delay_ms":1500,"error_status":429,"error":"rate_limit"}`))
	if ev.Type != "system" || ev.Subtype != "api_retry" || ev.Retry == nil {
		t.Fatalf("event %+v", ev)
	}
	r := ev.Retry
	if !r.HasAttempt || r.Attempt != 2 || !r.HasMax || r.MaxRetries != 5 || r.RetryDelayMS != 1500 {
		t.Fatalf("retry %+v", r)
	}
	if r.ErrorStatus == nil || *r.ErrorStatus != 429 || r.ErrorStatusNull || r.NoResponse || r.Error != "rate_limit" {
		t.Fatalf("status/error %+v", r)
	}
}

func TestDecodeAPIRetryNullStatusAndNoResponse(t *testing.T) {
	r := DecodeLine([]byte(`{"type":"system","subtype":"api_retry","error_status":null}`)).Retry
	if r == nil || r.ErrorStatus != nil || !r.ErrorStatusNull {
		t.Fatalf("null status %+v", r)
	}
	r = DecodeLine([]byte(`{"type":"system","subtype":"api_retry","no_response":{"waited_ms":1200,"retry_wait_ms":300}}`)).Retry
	if r == nil || !r.NoResponse || r.NoResponseWaitedMS != 1200 || r.NoResponseRetryWaitMS != 300 || r.ErrorStatusNull {
		t.Fatalf("no_response %+v", r)
	}
}

// D32 Q6: a malformed or unknown shape is never an error that fails the run.
func TestDecodeAPIRetryLenient(t *testing.T) {
	for _, line := range []string{
		`{"type":"system","subtype":"api_retry","attempt":"x","max_retries":{},"retry_delay_ms":-1,"error_status":"nope","error":{"open":true},"no_response":5}`,
		`{"type":"system","subtype":"api_retry","attempt":1.5,"max_retries":null,"retry_delay_ms":1e400,"error_status":429.5}`,
		`{"type":"system","subtype":"api_retry"}`,
	} {
		ev := DecodeLine([]byte(line))
		if ev.Undecodable || ev.Retry == nil {
			t.Fatalf("lenient decode failed for %s: %+v", line, ev)
		}
	}
	// A no_response key that is present but not an object still signals network;
	// it is never a decode failure.
	r := DecodeLine([]byte(`{"type":"system","subtype":"api_retry","no_response":5}`)).Retry
	if r == nil || !r.NoResponse {
		t.Fatalf("present no_response dropped: %+v", r)
	}
}

func TestDecodeToolUseIDAndPairs(t *testing.T) {
	ev := DecodeLine([]byte(`{"type":"assistant","session_id":"u","message":{"content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"command":"ls"}},{"type":"text","text":"hi"}]}}`))
	if len(ev.Blocks) != 2 || ev.Blocks[0].Type != "tool_use" || ev.Blocks[0].ID != "toolu_01" || ev.Blocks[0].Name != "Bash" {
		t.Fatalf("blocks %+v", ev.Blocks)
	}
	res := DecodeLine([]byte(`{"type":"user","session_id":"u","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"ok"}]}}`))
	if len(res.ToolResults) != 1 || res.ToolResults[0].ToolUseID != "toolu_01" {
		t.Fatalf("tool results %+v", res.ToolResults)
	}
	// An over-long id is bounded like every other display field.
	long := strings.Repeat("a", 300)
	ev = DecodeLine([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"` + long + `","name":"Read"}]}}`))
	if len(ev.Blocks) != 1 || len(ev.Blocks[0].ID) != 128 {
		t.Fatalf("id bound %d", len(ev.Blocks[0].ID))
	}
}

func TestBlockToolUseIDJSONIsAdditive(t *testing.T) {
	b, err := json.Marshal(Block{Type: "tool_use", ID: "toolu_01", Name: "Read", Input: `{}`})
	if err != nil || !strings.Contains(string(b), `"id":"toolu_01"`) {
		t.Fatalf("block JSON %s %v", b, err)
	}
	b, _ = json.Marshal(Block{Type: "text", Text: "hi"})
	if strings.Contains(string(b), `"id"`) {
		t.Fatalf("an empty id is not omitted: %s", b)
	}
}
