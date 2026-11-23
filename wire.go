package plugin

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ---- OpenAI chat-completions request parsing -------------------------------

// openaiRequest is the subset of the host's OpenAI payload this executor uses.
type openaiRequest struct {
	Model    string          `json:"model"`
	Messages []openaiMessage `json:"messages"`
	Tools    []struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	} `json:"tools"`
	MaxTokens        *int64   `json:"max_tokens"`
	MaxCompletionTok *int64   `json:"max_completion_tokens"`
	Temperature      *float64 `json:"temperature"`
	ReasoningEffort  string   `json:"reasoning_effort"`
}

// openaiMessage: Content stays raw — its shape varies per role
// (string | part array | null).
type openaiMessage struct {
	Role             string           `json:"role"`
	Content          json.RawMessage  `json:"content"`
	ToolCallID       string           `json:"tool_call_id"`
	ToolCalls        []openaiToolCall `json:"tool_calls"`
	ReasoningContent string           `json:"reasoning_content"`
}

type openaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// openaiContentPart is one block of an OpenAI content array.
type openaiContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url"`
	// Image is the gateway-format passthrough ({type:"image", image:"data:..."}).
	Image string `json:"image"`
}

// maxImageDataURLBytes caps a single image data URL (base64 payload
// included). 20 MiB covers multi-megapixel photos while keeping one
// request bounded.
const maxImageDataURLBytes = 20 << 20

// parseContentParts unmarshals an OpenAI content field. A JSON string yields
// one text part; a part array yields its blocks; anything else yields none.
func parseContentParts(raw json.RawMessage) []openaiContentPart {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil
		}
		return []openaiContentPart{{Type: "text", Text: s}}
	}
	var parts []openaiContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil
	}
	return parts
}

// resolveImageURL validates an image reference for the gateway. Only data:
// image URLs are accepted: the gateway takes a bare data URL, and this
// transport has no download step, so a remote http(s) URL is rejected with
// a clear error instead of being passed through as-is.
func resolveImageURL(url string) (string, error) {
	u := strings.TrimSpace(url)
	switch {
	case strings.HasPrefix(u, "data:image/"):
		if !strings.Contains(u, ";base64,") {
			return "", fmt.Errorf("commandcode-go: image data URL must be base64-encoded (data:<media-type>;base64,...)")
		}
		if len(u) > maxImageDataURLBytes {
			return "", fmt.Errorf("commandcode-go: image data URL exceeds %d bytes", maxImageDataURLBytes)
		}
		return u, nil
	case strings.HasPrefix(u, "data:"):
		return "", fmt.Errorf("commandcode-go: unsupported image data URL media type (only data:image/* is accepted)")
	case strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://"):
		return "", fmt.Errorf("commandcode-go: remote image URLs are not supported; download the image and send it as a data: URL")
	default:
		return "", fmt.Errorf("commandcode-go: unsupported image URL (expected a data:image/...;base64 URL)")
	}
}

// contentParts parses an OpenAI content field into gateway parts, preserving
// text and converting image_url parts into the gateway's
// {type:"image", image:"data:..."} form (the same shape the DSH plugin
// sends). It returns an error for image parts that cannot be sent, so a bad
// image fails loudly instead of being silently dropped.
func contentParts(raw json.RawMessage) ([]ccPart, error) {
	out := make([]ccPart, 0)
	for _, p := range parseContentParts(raw) {
		switch {
		case p.Type == "image_url" && p.ImageURL != nil:
			dataURL, err := resolveImageURL(p.ImageURL.URL)
			if err != nil {
				return nil, err
			}
			out = append(out, ccPart{Type: "image", Image: dataURL})
		case p.Type == "image" && p.Image != "":
			dataURL, err := resolveImageURL(p.Image)
			if err != nil {
				return nil, err
			}
			out = append(out, ccPart{Type: "image", Image: dataURL})
		case p.Text != "":
			out = append(out, ccPart{Type: "text", Text: p.Text})
		}
	}
	return out, nil
}

// contentText flattens an OpenAI content field into plain text, for roles
// the gateway keeps text-only (system, assistant, tool results). Image parts
// degrade to an explicit marker instead of being silently dropped.
func contentText(raw json.RawMessage) string {
	var b strings.Builder
	for _, p := range parseContentParts(raw) {
		switch {
		case p.Text != "":
			b.WriteString(p.Text)
		case p.Type == "image_url" && p.ImageURL != nil, p.Type == "image" && p.Image != "":
			b.WriteString("\n[image omitted: images are only supported in user messages]\n")
		}
	}
	return b.String()
}

// maxTokens resolves the effective completion budget (client value or the
// vendor CLI's own default).
func (r *openaiRequest) maxTokens() int64 {
	switch {
	case r.MaxCompletionTok != nil && *r.MaxCompletionTok > 0:
		return *r.MaxCompletionTok
	case r.MaxTokens != nil && *r.MaxTokens > 0:
		return *r.MaxTokens
	default:
		return 65536
	}
}

// ---- /alpha/generate request building --------------------------------------

// ccPart is one content block of a generate-wire message. Which fields are
// populated depends on Type: text/reasoning carry Text; image carries a
// base64 data URL in Image; tool-call carries ToolCallID/ToolName/Input;
// tool-result carries ToolCallID/ToolName/Output.
type ccPart struct {
	Type       string         `json:"type"`
	Text       string         `json:"text,omitempty"`
	Image      string         `json:"image,omitempty"`
	ToolCallID string         `json:"toolCallId,omitempty"`
	ToolName   string         `json:"toolName,omitempty"`
	Input      map[string]any `json:"input,omitempty"`
	Output     *ccToolOutput  `json:"output,omitempty"`
}

type ccMessage struct {
	Role    string   `json:"role"` // user | assistant | tool
	Content []ccPart `json:"content"`
}

type ccTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// buildGenerateBody converts the host's OpenAI payload into the
// /alpha/generate request body. Upstream is always called with stream:true —
// the wire has no non-streaming mode; the executor assembles non-stream
// responses from the same event flow.
func buildGenerateBody(upstreamModel string, payload []byte) ([]byte, error) {
	var req openaiRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("commandcode-go: cannot parse openai payload: %w", err)
	}

	// Tool-call id remap: the gateway rejects ids longer than 64 chars
	// (input[N].call_id <= 64). Cross-provider histories can carry longer
	// ids, so overlong ids get short per-request aliases; the alias is
	// applied to the call and its result alike so the pair stays correlated.
	aliases := map[string]string{}
	toolNames := map[string]string{}
	callIDs := map[string]bool{}
	for _, m := range req.Messages {
		for _, tc := range m.ToolCalls {
			toolNames[tc.ID] = tc.Function.Name
			callIDs[tc.ID] = true
		}
	}
	wireID := func(id string) string {
		if len(id) <= 64 {
			return id
		}
		if a, ok := aliases[id]; ok {
			return a
		}
		a := "cc-" + strconv.Itoa(len(aliases)+1)
		aliases[id] = a
		return a
	}

	var system strings.Builder
	messages := make([]ccMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		switch m.Role {
		case "system", "developer":
			if t := contentText(m.Content); t != "" {
				if system.Len() > 0 {
					system.WriteString("\n\n")
				}
				system.WriteString(t)
			}
		case "user":
			parts, err := contentParts(m.Content)
			if err != nil {
				return nil, err
			}
			if len(parts) > 0 {
				messages = append(messages, ccMessage{Role: "user", Content: parts})
			}
		case "assistant":
			var parts []ccPart
			if m.ReasoningContent != "" {
				// The thinking block MUST ride along on rebuilt assistant
				// turns: DeepSeek thinking-mode tool loops are rejected
				// without it (the official CLI sends the same shape).
				parts = append(parts, ccPart{Type: "reasoning", Text: m.ReasoningContent})
			}
			if t := contentText(m.Content); t != "" {
				parts = append(parts, ccPart{Type: "text", Text: t})
			}
			for _, tc := range m.ToolCalls {
				input := map[string]any{}
				if raw := tc.Function.Arguments; strings.TrimSpace(raw) != "" {
					_ = json.Unmarshal([]byte(raw), &input)
					if input == nil {
						input = map[string]any{}
					}
				}
				parts = append(parts, ccPart{Type: "tool-call",
					ToolCallID: wireID(tc.ID), ToolName: tc.Function.Name, Input: input})
			}
			if len(parts) > 0 {
				messages = append(messages, ccMessage{Role: "assistant", Content: parts})
			}
		case "tool":
			if !callIDs[m.ToolCallID] {
				// A result with no matching call cannot be correlated by the
				// gateway; dropping mirrors the reference implementation.
				continue
			}
			value := contentText(m.Content)
			if value == "" {
				value = "(empty tool result)"
			}
			messages = append(messages, ccMessage{Role: "tool",
				Content: []ccPart{{Type: "tool-result",
					ToolCallID: wireID(m.ToolCallID),
					ToolName:   toolNameOrUnknown(toolNames, m.ToolCallID),
					Output:     &ccToolOutput{Type: "text", Value: value}}}})
		}
	}

	tools := make([]ccTool, 0, len(req.Tools))
	for _, t := range req.Tools {
		if t.Function.Name == "" {
			continue
		}
		schema := t.Function.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		tools = append(tools, ccTool{Type: "function", Name: t.Function.Name,
			Description: t.Function.Description, InputSchema: schema})
	}

	effort := strings.TrimSpace(req.ReasoningEffort)
	if effort == "minimal" {
		effort = "low"
	}

	body := map[string]any{
		"config": map[string]any{
			"workingDir":    "/tmp",
			"date":          time.Now().UTC().Format("2006-01-02"),
			"environment":   "cli-proxy-api commandcode-go",
			"structure":     []any{},
			"isGitRepo":     false,
			"currentBranch": "",
			"mainBranch":    "",
			"gitStatus":     "",
			"recentCommits": []any{},
		},
		"memory": nil,
		"taste":  nil,
		"skills": nil,
		"params": map[string]any{
			"model":      upstreamModel,
			"messages":   messages,
			"tools":      tools,
			"system":     system.String(),
			"max_tokens": req.maxTokens(),
			"stream":     true,
			// temperature: forward only when the client set it; the vendor
			// default applies otherwise.
		},
		"threadId": newUUID(),
	}
	params := body["params"].(map[string]any)
	if req.Temperature != nil {
		params["temperature"] = *req.Temperature
	}
	if effort != "" {
		params["reasoning_effort"] = effort
	}
	return json.Marshal(body)
}

func toolNameOrUnknown(names map[string]string, id string) string {
	if n := names[id]; n != "" {
		return n
	}
	return "unknown"
}

// ccToolOutput is the tool-result output block (declared separately so the
// generic ccPart above can stay flat).
type ccToolOutput struct {
	Type  string `json:"type"` // text | error-text
	Value string `json:"value"`
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ---- generate events -> OpenAI responses -----------------------------------

// generateEvent is one JSONL line of the /alpha/generate reply.
type generateEvent struct {
	Type string `json:"type"`
	// text-delta / reasoning-delta
	Text string `json:"text"`
	// tool-call
	ToolCallID string         `json:"toolCallId"`
	ToolName   string         `json:"toolName"`
	Input      map[string]any `json:"input"`
	Args       map[string]any `json:"args"`
	Arguments  map[string]any `json:"arguments"`
	// finish
	FinishReason string         `json:"finishReason"`
	TotalUsage   *generateUsage `json:"totalUsage"`
	// error: error may be an object or a string
	Error   json.RawMessage `json:"error"`
	Message string          `json:"message"`
}

// generateUsage retains all upstream token accounting.
type generateUsage = struct {
	InputTokens       *int64 `json:"inputTokens"`
	OutputTokens      *int64 `json:"outputTokens"`
	InputTokenDetails *struct {
		CacheReadTokens  *int64 `json:"cacheReadTokens"`
		CacheWriteTokens *int64 `json:"cacheWriteTokens"`
		NoCacheTokens    *int64 `json:"noCacheTokens"`
	} `json:"inputTokenDetails"`
}

// openaiUsage preserves cache reads separately from cache creation.
func openaiUsage(usage *generateUsage) map[string]any {
	prompt, completion := int64(0), int64(0)
	if usage != nil {
		if usage.InputTokens != nil {
			prompt = *usage.InputTokens
		}
		if usage.OutputTokens != nil {
			completion = *usage.OutputTokens
		}
	}
	result := map[string]any{"prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": prompt + completion}
	if usage != nil && usage.InputTokenDetails != nil {
		details := usage.InputTokenDetails
		if details.CacheReadTokens != nil {
			result["prompt_tokens_details"] = map[string]any{"cached_tokens": *details.CacheReadTokens}
			result["cache_read_input_tokens"] = *details.CacheReadTokens
		}
		// Cache creation has no standard Chat Completions field.
		if details.CacheWriteTokens != nil {
			result["cache_creation_input_tokens"] = *details.CacheWriteTokens
		}
		if details.NoCacheTokens != nil {
			result["prompt_cache_miss_tokens"] = *details.NoCacheTokens
		}
	}
	return result
}

func (e *generateEvent) errorMessage() string {
	if len(e.Error) > 0 {
		var obj struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(e.Error, &obj); err == nil && obj.Message != "" {
			return obj.Message
		}
		var s string
		if err := json.Unmarshal(e.Error, &s); err == nil && s != "" {
			return s
		}
		return string(e.Error)
	}
	return e.Message
}

func (e *generateEvent) toolArguments() map[string]any {
	switch {
	case e.Input != nil:
		return e.Input
	case e.Args != nil:
		return e.Args
	default:
		return e.Arguments
	}
}

// mapFinishReason normalizes the vendor's finish reason onto the OpenAI one.
func mapFinishReason(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "tool-calls", "tool_calls":
		return "tool_calls"
	case "length", "max_tokens", "max-tokens", "max_output_tokens":
		return "length"
	default:
		return "stop"
	}
}

// chunkBuilder emits OpenAI chat-completion chunks (bare JSON payloads; the
// host adds "data:" framing downstream).
type chunkBuilder struct {
	id        string
	model     string
	created   int64
	toolIndex int
}

func newChunkBuilder(model string) *chunkBuilder {
	return &chunkBuilder{id: "chatcmpl-" + newUUID(), model: model, created: time.Now().Unix()}
}

func (c *chunkBuilder) frame(delta map[string]any, finishReason any) ([]byte, error) {
	return json.Marshal(map[string]any{
		"id":      c.id,
		"object":  "chat.completion.chunk",
		"created": c.created,
		"model":   c.model,
		"choices": []map[string]any{
			{"index": 0, "delta": delta, "finish_reason": finishReason},
		},
	})
}

// roleChunk opens the stream with the assistant role marker.
func (c *chunkBuilder) roleChunk() ([]byte, error) {
	return c.frame(map[string]any{"role": "assistant", "content": ""}, nil)
}

func (c *chunkBuilder) textChunk(text string) ([]byte, error) {
	return c.frame(map[string]any{"content": text}, nil)
}

func (c *chunkBuilder) reasoningChunk(text string) ([]byte, error) {
	return c.frame(map[string]any{"reasoning_content": text}, nil)
}

func (c *chunkBuilder) toolCallChunk(id, name string, input map[string]any) ([]byte, error) {
	args, err := json.Marshal(input)
	if err != nil {
		args = []byte("{}")
	}
	idx := c.toolIndex
	c.toolIndex++
	return c.frame(map[string]any{"tool_calls": []map[string]any{{
		"index": idx, "id": id, "type": "function",
		"function": map[string]any{"name": name, "arguments": string(args)},
	}}}, nil)
}

func (c *chunkBuilder) finishChunk(reason string) ([]byte, error) {
	return c.frame(map[string]any{}, reason)
}

// usageChunk carries token counts on a trailing empty-choices chunk (the
// OpenAI stream_options.include_usage shape the host reads).
func (c *chunkBuilder) usageChunk(usage *generateUsage) ([]byte, error) {
	return json.Marshal(map[string]any{
		"id":      c.id,
		"object":  "chat.completion.chunk",
		"created": c.created,
		"model":   c.model,
		"choices": []any{},
		"usage":   openaiUsage(usage),
	})
}

// assembleCompletion drains the event stream into one non-streaming OpenAI
// chat.completion object.
type completionAssembler struct {
	id        string
	model     string
	created   int64
	content   strings.Builder
	reasoning strings.Builder
	toolCalls []map[string]any
	finish    string
	usage     *generateUsage
}

func newCompletionAssembler(model string) *completionAssembler {
	return &completionAssembler{id: "chatcmpl-" + newUUID(), model: model, created: time.Now().Unix()}
}

func (a *completionAssembler) apply(ev *generateEvent) {
	switch ev.Type {
	case "text-delta":
		a.content.WriteString(ev.Text)
	case "reasoning-delta":
		a.reasoning.WriteString(ev.Text)
	case "tool-call":
		id := ev.ToolCallID
		if id == "" {
			id = "call_" + newUUID()
		}
		args, err := json.Marshal(ev.toolArguments())
		if err != nil {
			args = []byte("{}")
		}
		a.toolCalls = append(a.toolCalls, map[string]any{
			"id": id, "type": "function",
			"function": map[string]any{"name": ev.ToolName, "arguments": string(args)},
		})
	case "finish":
		a.finish = mapFinishReason(ev.FinishReason)
		if ev.TotalUsage != nil {
			a.usage = ev.TotalUsage
		}
	}
}

func (a *completionAssembler) render() ([]byte, error) {
	finish := a.finish
	if finish == "" {
		finish = "stop"
	}
	message := map[string]any{"role": "assistant", "content": a.content.String()}
	if a.reasoning.Len() > 0 {
		message["reasoning_content"] = a.reasoning.String()
	}
	if len(a.toolCalls) > 0 {
		message["tool_calls"] = a.toolCalls
	}
	return json.Marshal(map[string]any{
		"id":      a.id,
		"object":  "chat.completion",
		"created": a.created,
		"model":   a.model,
		"choices": []map[string]any{
			{"index": 0, "message": message, "finish_reason": finish},
		},
		"usage": openaiUsage(a.usage),
	})
}
