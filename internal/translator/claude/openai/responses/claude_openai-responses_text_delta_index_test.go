package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// With thinking first, Claude streams the thinking block at index 0 and the text
// block at index 1, and the message item is opened at output_index 1. Every
// output_text.delta must carry that same output_index: clients such as pi route
// deltas by output_index, found the reasoning slot at 0 and dropped the text, so
// UIs that render from deltas showed nothing for turns that had thinking.
func TestClaudeToResponsesTextDeltaUsesMessageOutputIndex(t *testing.T) {
	chunks := []string{
		`data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":5,"output_tokens":1}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"let me think"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"The answer"}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":" is 5."}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`,
		`data: {"type":"message_stop"}`,
	}
	var param any
	var events []string
	for _, chunk := range chunks {
		events = append(events, ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-opus-5-5", nil, nil, []byte(chunk), &param)...)
	}

	messageIndex := int64(-1)
	deltas := 0
	for _, event := range events {
		lines := strings.SplitN(event, "\n", 2)
		if len(lines) != 2 {
			continue
		}
		payload := strings.TrimPrefix(lines[1], "data: ")
		switch gjson.Get(payload, "type").String() {
		case "response.output_item.added":
			if gjson.Get(payload, "item.type").String() == "message" {
				messageIndex = gjson.Get(payload, "output_index").Int()
			}
		case "response.output_text.delta":
			deltas++
			if got := gjson.Get(payload, "output_index").Int(); got != messageIndex {
				t.Fatalf("output_text.delta output_index = %d, want the message item's %d", got, messageIndex)
			}
		}
	}
	if messageIndex != 1 {
		t.Fatalf("message output_index = %d, want 1 (after the reasoning item)", messageIndex)
	}
	if deltas != 2 {
		t.Fatalf("output_text.delta events = %d, want 2", deltas)
	}
}
