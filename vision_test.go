package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

// wireTestPart mirrors one ccPart as decoded back from a built body.
type wireTestPart struct {
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	Image      string          `json:"image"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Input      json.RawMessage `json:"input"`
	Output     *struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"output"`
}

type wireTestMessage struct {
	Role    string         `json:"role"`
	Content []wireTestPart `json:"content"`
}

// buildMessages builds a generate body and returns its decoded messages.
func buildMessages(t *testing.T, upstream, payload string) []wireTestMessage {
	t.Helper()
	raw, err := buildGenerateBody(upstream, []byte(payload))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var body struct {
		Params struct {
			Messages []wireTestMessage `json:"messages"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return body.Params.Messages
}

func TestContentParts_PureText(t *testing.T) {
	// Plain string content stays one text part.
	parts, err := contentParts(json.RawMessage(`"hello"`))
	if err != nil || len(parts) != 1 || parts[0].Type != "text" || parts[0].Text != "hello" {
		t.Fatalf("string content = %+v err=%v", parts, err)
	}
	// A text-only part array stays text-only.
	parts, err = contentParts(json.RawMessage(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`))
	if err != nil || len(parts) != 2 || parts[0].Text != "a" || parts[1].Text != "b" {
		t.Fatalf("text array = %+v err=%v", parts, err)
	}
	// Empty / null content yields no parts and no error.
	if parts, err := contentParts(json.RawMessage(``)); err != nil || len(parts) != 0 {
		t.Errorf("empty = %+v err=%v", parts, err)
	}
	if parts, err := contentParts(json.RawMessage(`null`)); err != nil || len(parts) != 0 {
		t.Errorf("null = %+v err=%v", parts, err)
	}
}

func TestContentParts_ImageFormats(t *testing.T) {
	png := "data:image/png;base64,iVBORw0KGgo="
	jpeg := "data:image/jpeg;base64,/9j/4AAQSkZJRg=="
	// OpenAI image_url -> gateway image form, PNG and JPEG.
	parts, err := contentParts(json.RawMessage(`[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"` + png + `"}}]`))
	if err != nil {
		t.Fatalf("png: %v", err)
	}
	if len(parts) != 2 || parts[0].Type != "text" || parts[0].Text != "look" || parts[1].Type != "image" || parts[1].Image != png {
		t.Errorf("png parts = %+v", parts)
	}
	parts, err = contentParts(json.RawMessage(`[{"type":"image_url","image_url":{"url":"` + jpeg + `"}}]`))
	if err != nil || len(parts) != 1 || parts[0].Type != "image" || parts[0].Image != jpeg {
		t.Fatalf("jpeg parts = %+v err=%v", parts, err)
	}
	// Gateway-format image part passes through unchanged.
	parts, err = contentParts(json.RawMessage(`[{"type":"image","image":"` + png + `"}]`))
	if err != nil || len(parts) != 1 || parts[0].Type != "image" || parts[0].Image != png {
		t.Fatalf("gateway image = %+v err=%v", parts, err)
	}
}

func TestContentParts_RejectedImages(t *testing.T) {
	cases := map[string]string{
		"remote http":  `[{"type":"image_url","image_url":{"url":"https://example.com/cat.png"}}]`,
		"remote https": `[{"type":"image_url","image_url":{"url":"http://example.com/cat.png"}}]`,
		"non-image":    `[{"type":"image_url","image_url":{"url":"data:text/plain;base64,aGk="}}]`,
		"not base64":   `[{"type":"image_url","image_url":{"url":"data:image/png,rawbytes"}}]`,
		"empty url":    `[{"type":"image_url","image_url":{"url":""}}]`,
	}
	for name, raw := range cases {
		if _, err := contentParts(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: expected error, got none", name)
		}
	}
	// Oversize data URL is rejected (20 MiB cap).
	big := "data:image/png;base64," + strings.Repeat("A", maxImageDataURLBytes)
	if _, err := contentParts(json.RawMessage(`[{"type":"image_url","image_url":{"url":"` + big + `"}}]`)); err == nil {
		t.Errorf("oversize: expected error, got none")
	}
	// A rejected image fails the whole build with a clear error.
	if _, err := buildGenerateBody("deepseek/deepseek-v4.1-flash", []byte(`{
		"model": "commandcode-go/deepseek-v4.1-flash",
		"messages": [{"role": "user", "content": [{"type": "image_url", "image_url": {"url": "https://example.com/cat.png"}}]}]
	}`)); err == nil || !strings.Contains(err.Error(), "remote image URLs are not supported") {
		t.Errorf("build with remote image: err = %v", err)
	}
}

func TestContentText_ImageMarkerInNonUserRoles(t *testing.T) {
	// Images in a system message still flatten to a marker (the gateway
	// keeps system text-only); only user messages carry image parts.
	got := contentText(json.RawMessage(`[{"type":"text","text":"sys"},{"type":"image_url","image_url":{"url":"data:image/png;base64,xx"}}]`))
	if !strings.Contains(got, "sys") || !strings.Contains(got, "[image omitted") {
		t.Errorf("system text = %q", got)
	}
}

func TestBuildGenerateBody_ImagePlusToolCall(t *testing.T) {
	msgs := buildMessages(t, "deepseek/deepseek-v4.1-flash", `{
		"model": "commandcode-go/deepseek-v4.1-flash",
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "what color is each cell?"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,GRID"}}
			]},
			{"role": "assistant", "content": "checking",
			 "tool_calls": [{"id": "call-1", "type": "function", "function": {"name": "describe", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "call-1", "content": "red green blue"}
		],
		"tools": [{"type": "function", "function": {"name": "describe", "description": "d", "parameters": {"type": "object"}}}]
	}`)
	if len(msgs) != 3 {
		t.Fatalf("messages = %d", len(msgs))
	}
	// User: text + image ride together in one message.
	user := msgs[0]
	if user.Role != "user" || len(user.Content) != 2 {
		t.Fatalf("user = %+v", user)
	}
	if user.Content[0].Type != "text" || user.Content[0].Text != "what color is each cell?" {
		t.Errorf("user text = %+v", user.Content[0])
	}
	if user.Content[1].Type != "image" || user.Content[1].Image != "data:image/png;base64,GRID" {
		t.Errorf("user image = %+v", user.Content[1])
	}
	// Assistant: text + tool-call share the turn, tool result correlates.
	asm := msgs[1]
	var sawCall bool
	for _, p := range asm.Content {
		if p.Type == "tool-call" && p.ToolCallID == "call-1" && p.ToolName == "describe" {
			sawCall = true
		}
	}
	if !sawCall {
		t.Errorf("assistant parts = %+v", asm.Content)
	}
	if msgs[2].Role != "tool" || len(msgs[2].Content) != 1 || msgs[2].Content[0].ToolCallID != "call-1" {
		t.Errorf("tool message = %+v", msgs[2])
	}
}

func TestSupportedModalities(t *testing.T) {
	vision := []string{
		"deepseek/deepseek-v4.1-flash",
		"deepseek/deepseek-v4-flash-vision-exp",
		"moonshotai/Kimi-K2.5",
		"moonshotai/Kimi-K3",
		"claude-sonnet-5",
		"gpt-5.4",
		"z-ai/glm-5.3-flash",
		"DeepSeek/DeepSeek-V4.1-Flash", // case-insensitive lookup
	}
	for _, id := range vision {
		got := supportedModalities(id)
		if len(got) != 2 || got[0] != "text" || got[1] != "image" {
			t.Errorf("supportedModalities(%q) = %v, want [text image]", id, got)
		}
	}
	textOnly := []string{
		"deepseek/deepseek-v4-flash",
		"deepseek/deepseek-v4-flash-fast",
		"deepseek/deepseek-v4-pro",
		"deepseek/deepseek-v4.1-flash-fast",
		"zai-org/GLM-5.3",
		"some/unknown-model",
		"",
	}
	for _, id := range textOnly {
		got := supportedModalities(id)
		if len(got) != 1 || got[0] != "text" {
			t.Errorf("supportedModalities(%q) = %v, want [text]", id, got)
		}
	}
}

func TestModelResponse_VisionModalities(t *testing.T) {
	entries := []modelEntry{
		{Upstream: "deepseek/deepseek-v4.1-flash", Alias: "deepseek-v4.1-flash", Display: "V4.1 Flash", Available: true},
		{Upstream: "deepseek/deepseek-v4-flash", Alias: "deepseek-v4-flash", Display: "V4 Flash", Available: true},
	}
	resp := modelResponse(entries)
	if len(resp.Models) != 2 {
		t.Fatalf("models = %d", len(resp.Models))
	}
	if got := resp.Models[0].SupportedInputModalities; len(got) != 2 || got[1] != "image" {
		t.Errorf("v4.1-flash modalities = %v", got)
	}
	if got := resp.Models[1].SupportedInputModalities; len(got) != 1 || got[0] != "text" {
		t.Errorf("v4-flash modalities = %v", got)
	}
}
