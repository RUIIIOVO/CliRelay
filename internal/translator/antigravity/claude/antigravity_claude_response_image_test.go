package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// imageResponseChunk is an image model's answer: one inline image part and no
// text at all.
const imageResponseChunk = `{
	"response": {
		"responseId": "resp-image-1",
		"modelVersion": "gemini-3.1-flash-image",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"inlineData": {"mimeType": "image/jpeg", "data": "QUJD"}}]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 16, "candidatesTokenCount": 1550, "totalTokenCount": 1566}
	}
}`

const imageRequestJSON = `{"model": "gemini-3.1-flash-image", "messages": [{"role": "user", "content": "draw an apple"}]}`

func TestConvertAntigravityResponseToClaude_InlineImage(t *testing.T) {
	var param any
	ctx := context.Background()
	request := []byte(imageRequestJSON)

	out := ConvertAntigravityResponseToClaude(ctx, "gemini-3.1-flash-image", request, request, []byte(imageResponseChunk), &param)
	out = append(out, ConvertAntigravityResponseToClaude(ctx, "gemini-3.1-flash-image", request, request, []byte("[DONE]"), &param)...)

	stream := strings.Join(out, "")
	if !strings.Contains(stream, "event: content_block_start") {
		t.Fatalf("expected an image content block, got: %s", stream)
	}
	if !strings.Contains(stream, "event: message_stop") {
		t.Fatalf("expected the stream to be closed with message_stop, got: %s", stream)
	}

	var imageBlock gjson.Result
	for _, chunk := range out {
		for _, line := range strings.Split(chunk, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := gjson.Parse(strings.TrimPrefix(line, "data: "))
			if payload.Get("content_block.type").String() == "image" {
				imageBlock = payload.Get("content_block")
			}
		}
	}
	if !imageBlock.Exists() {
		t.Fatalf("no image content block in stream: %s", stream)
	}
	if got := imageBlock.Get("source.type").String(); got != "base64" {
		t.Fatalf("expected a base64 source, got %q", got)
	}
	if got := imageBlock.Get("source.media_type").String(); got != "image/jpeg" {
		t.Fatalf("expected media_type image/jpeg, got %q", got)
	}
	if got := imageBlock.Get("source.data").String(); got != "QUJD" {
		t.Fatalf("expected the image bytes to survive, got %q", got)
	}
}

func TestConvertAntigravityResponseToClaudeNonStream_InlineImage(t *testing.T) {
	var param any
	request := []byte(imageRequestJSON)

	got := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "gemini-3.1-flash-image", request, request, []byte(imageResponseChunk), &param)

	content := gjson.Get(got, "content")
	if !content.IsArray() || len(content.Array()) != 1 {
		t.Fatalf("expected exactly one content block, got: %s", got)
	}
	block := content.Array()[0]
	if blockType := block.Get("type").String(); blockType != "image" {
		t.Fatalf("expected an image block, got %q: %s", blockType, got)
	}
	if got := block.Get("source.media_type").String(); got != "image/jpeg" {
		t.Fatalf("expected media_type image/jpeg, got %q", got)
	}
	if got := block.Get("source.data").String(); got != "QUJD" {
		t.Fatalf("expected the image bytes to survive, got %q", got)
	}
	if stop := gjson.Get(got, "stop_reason").String(); stop != "end_turn" {
		t.Fatalf("expected stop_reason end_turn, got %q", stop)
	}
}

// An image can arrive alongside text, and the text must keep its own block.
func TestConvertAntigravityResponseToClaudeNonStream_TextAndImage(t *testing.T) {
	var param any
	request := []byte(imageRequestJSON)
	chunk := []byte(`{
		"response": {
			"candidates": [{
				"content": {"parts": [
					{"text": "here you go"},
					{"inline_data": {"mime_type": "image/png", "data": "REVG"}}
				]},
				"finishReason": "STOP"
			}]
		}
	}`)

	got := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "gemini-3.1-flash-image", request, request, chunk, &param)

	blocks := gjson.Get(got, "content").Array()
	if len(blocks) != 2 {
		t.Fatalf("expected a text block and an image block, got: %s", got)
	}
	if blocks[0].Get("type").String() != "text" || blocks[0].Get("text").String() != "here you go" {
		t.Fatalf("unexpected first block: %s", blocks[0].Raw)
	}
	if blocks[1].Get("type").String() != "image" || blocks[1].Get("source.data").String() != "REVG" {
		t.Fatalf("unexpected second block: %s", blocks[1].Raw)
	}
}
