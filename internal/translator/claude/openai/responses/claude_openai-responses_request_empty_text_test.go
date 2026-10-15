package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

const emptyTextTestImage = `{"type":"input_image","detail":"auto","image_url":"data:image/png;base64,iVBORw0KGgo="}`

// An image sent without any typed text arrives as an empty input_text next to
// the input_image (pi does this). Anthropic rejects the empty text block with
// "messages: text content blocks must be non-empty", so only the image may go.
func TestConvertOpenAIResponsesRequestToClaude_ImageWithEmptyTextDropsTextBlock(t *testing.T) {
	in := []byte(`{"model":"claude-sonnet-5-5","input":[{"type":"message","role":"user","content":[
		{"type":"input_text","text":""},` + emptyTextTestImage + `]}]}`)
	out := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-5-5", in, false)
	content := gjson.GetBytes(out, "messages.0.content")
	if !content.IsArray() || len(content.Array()) != 1 || content.Get("0.type").String() != "image" {
		t.Fatalf("content = %s, want only the image block", content.Raw)
	}
	if role := gjson.GetBytes(out, "messages.0.role").String(); role != "user" {
		t.Fatalf("role = %q, want user", role)
	}
}

// Anthropic accepts a whitespace-only text block, so it is passed through.
func TestConvertOpenAIResponsesRequestToClaude_ImageWithWhitespaceTextKept(t *testing.T) {
	in := []byte(`{"model":"claude-sonnet-5-5","input":[{"type":"message","role":"user","content":[
		{"type":"input_text","text":"  "},` + emptyTextTestImage + `]}]}`)
	out := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-5-5", in, false)
	content := gjson.GetBytes(out, "messages.0.content")
	if len(content.Array()) != 2 || content.Get("0.text").String() != "  " {
		t.Fatalf("content = %s, want the whitespace text block kept before the image", content.Raw)
	}
}

// Text that carries content is kept next to the image, as before.
func TestConvertOpenAIResponsesRequestToClaude_ImageWithTextKeepsBoth(t *testing.T) {
	in := []byte(`{"model":"claude-sonnet-5-5","input":[{"type":"message","role":"user","content":[
		{"type":"input_text","text":"what is this?"},` + emptyTextTestImage + `]}]}`)
	out := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-5-5", in, false)
	content := gjson.GetBytes(out, "messages.0.content")
	if len(content.Array()) != 2 || content.Get("0.text").String() != "what is this?" || content.Get("1.type").String() != "image" {
		t.Fatalf("content = %s, want the text block then the image block", content.Raw)
	}
}

// A plain text message keeps the legacy string content.
func TestConvertOpenAIResponsesRequestToClaude_PlainTextUnchanged(t *testing.T) {
	in := []byte(`{"model":"claude-sonnet-5-5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	out := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-5-5", in, false)
	if got := gjson.GetBytes(out, "messages.0.content"); got.Type != gjson.String || got.String() != "hi" {
		t.Fatalf("content = %s, want the string \"hi\"", got.Raw)
	}
}
