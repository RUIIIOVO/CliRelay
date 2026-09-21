package responses

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

// geminiImageChunk is an image model's answer: one inline image part, no text.
const geminiImageChunk = `{
	"responseId": "resp-image-1",
	"modelVersion": "gemini-3.1-flash-image",
	"candidates": [{
		"content": {"role": "model", "parts": [{"inlineData": {"mimeType": "image/jpeg", "data": "QUJD"}}]},
		"finishReason": "STOP"
	}],
	"usageMetadata": {"promptTokenCount": 16, "candidatesTokenCount": 1550, "totalTokenCount": 1566}
}`

const geminiImageRequest = `{"model": "gemini-3.1-flash-image", "input": "draw an apple"}`

func TestConvertGeminiResponseToOpenAIResponses_InlineImage(t *testing.T) {
	var param any
	request := []byte(geminiImageRequest)

	out := ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.1-flash-image", request, request, []byte(geminiImageChunk), &param)

	var sawAdded, sawDone, sawCompleted bool
	for _, chunk := range out {
		event, data := parseSSEEvent(t, chunk)
		switch event {
		case "response.output_item.added":
			if data.Get("item.type").String() == "image_generation_call" {
				sawAdded = true
				if status := data.Get("item.status").String(); status != "in_progress" {
					t.Fatalf("expected the image item to open in_progress, got %q", status)
				}
			}
		case "response.output_item.done":
			if data.Get("item.type").String() == "image_generation_call" {
				sawDone = true
				if got := data.Get("item.result").String(); got != "QUJD" {
					t.Fatalf("expected the image bytes in item.result, got %q", got)
				}
				if got := data.Get("item.output_format").String(); got != "jpeg" {
					t.Fatalf("expected output_format jpeg, got %q", got)
				}
			}
		case "response.completed":
			sawCompleted = true
			if got := data.Get("response.output.0.type").String(); got != "image_generation_call" {
				t.Fatalf("expected response.output[0] to be the image, got: %s", data.Get("response.output").Raw)
			}
			if got := data.Get("response.output.0.result").String(); got != "QUJD" {
				t.Fatalf("expected the image bytes in the aggregated output, got %q", got)
			}
		}
	}

	if !sawAdded || !sawDone || !sawCompleted {
		t.Fatalf("missing image events: added=%v done=%v completed=%v", sawAdded, sawDone, sawCompleted)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesNonStream_InlineImage(t *testing.T) {
	var param any
	request := []byte(geminiImageRequest)

	got := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.1-flash-image", request, request, []byte(geminiImageChunk), &param)

	output := gjson.Get(got, "output")
	if !output.IsArray() || len(output.Array()) != 1 {
		t.Fatalf("expected exactly one output item, got: %s", got)
	}
	item := output.Array()[0]
	if itemType := item.Get("type").String(); itemType != "image_generation_call" {
		t.Fatalf("expected an image_generation_call, got %q: %s", itemType, got)
	}
	if result := item.Get("result").String(); result != "QUJD" {
		t.Fatalf("expected the image bytes to survive, got %q", result)
	}
	if status := item.Get("status").String(); status != "completed" {
		t.Fatalf("expected status completed, got %q", status)
	}
}

// Text and an image in the same answer must land as two separate output items,
// in the order the upstream sent them.
func TestConvertGeminiResponseToOpenAIResponsesNonStream_TextAndImage(t *testing.T) {
	var param any
	request := []byte(geminiImageRequest)
	chunk := []byte(`{
		"candidates": [{
			"content": {"parts": [
				{"text": "here you go"},
				{"inline_data": {"mime_type": "image/png", "data": "REVG"}}
			]},
			"finishReason": "STOP"
		}]
	}`)

	got := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.1-flash-image", request, request, chunk, &param)

	items := gjson.Get(got, "output").Array()
	if len(items) != 2 {
		t.Fatalf("expected an image item and a message item, got: %s", got)
	}
	if items[0].Get("type").String() != "image_generation_call" || items[0].Get("output_format").String() != "png" {
		t.Fatalf("unexpected first item: %s", items[0].Raw)
	}
	if items[1].Get("type").String() != "message" || items[1].Get("content.0.text").String() != "here you go" {
		t.Fatalf("unexpected second item: %s", items[1].Raw)
	}
}

func TestResponsesImageOutputFormat(t *testing.T) {
	cases := map[string]string{
		"image/jpeg":       "jpeg",
		"image/jpg":        "jpeg",
		"image/png":        "png",
		"image/webp":       "webp",
		"IMAGE/PNG":        "png",
		"image/png;base64": "png",
		"":                 "png",
	}
	for mimeType, want := range cases {
		if got := responsesImageOutputFormat(mimeType); got != want {
			t.Errorf("responsesImageOutputFormat(%q) = %q, want %q", mimeType, got, want)
		}
	}
}
