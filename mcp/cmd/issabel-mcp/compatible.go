package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

func normalizeBaseURL(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(value, "#") {
		return "", errors.New("base_url must be an HTTPS API base URL without credentials, query, or fragment")
	}
	if strings.HasSuffix(u.Path, "/chat/completions") {
		return "", errors.New("base_url must not include /chat/completions")
	}
	return value, nil
}

// Each factory call creates a fresh transcript for a single assistant request.
// Raw assistant messages preserve provider extensions such as reasoning details.
type compatibleProvider struct {
	client     *http.Client
	baseURL    string
	transcript []interface{}
	consumed   int
}

func newCompatibleProvider(baseURL string, client *http.Client) (*compatibleProvider, error) {
	baseURL, err := normalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &compatibleProvider{client: &copyClient, baseURL: baseURL}, nil
}

// Only locally generated IDs, fixed categories and numeric metadata reach logs.
// Never log endpoints, upstream errors, prompts, response text or credentials.
type providerDiagnostic struct {
	ID               string
	Operation        string
	Started          time.Time
	HTTP             int
	Outcome          string
	Finish           string
	Choices          int
	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int
}

func newProviderDiagnostic(operation string) *providerDiagnostic {
	return &providerDiagnostic{ID: randomHex(8), Operation: operation, Started: time.Now(), Outcome: "ok"}
}
func (d *providerDiagnostic) finish(err *error) {
	if *err != nil {
		if d.Outcome == "ok" {
			d.Outcome = "invalid_response"
		}
		*err = fmt.Errorf("%w (diagnostic_id=%s)", *err, d.ID)
	}
	log.Printf("provider_diagnostic id=%s provider=openai_compatible operation=%s outcome=%s http_status=%d duration_ms=%d finish_reason=%q choices=%d prompt_tokens=%d completion_tokens=%d reasoning_tokens=%d", d.ID, d.Operation, d.Outcome, d.HTTP, time.Since(d.Started).Milliseconds(), d.Finish, d.Choices, d.PromptTokens, d.CompletionTokens, d.ReasoningTokens)
}

func (p *compatibleProvider) request(req *http.Request, key string, out interface{}, diagnostic *providerDiagnostic) error {
	req.Header.Set("Authorization", "Bearer "+key)
	// Do not expose upstream bodies or transport errors: either may echo secrets.
	resp, err := p.client.Do(req)
	if err != nil {
		diagnostic.Outcome = "connection_error"
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			diagnostic.Outcome = "timeout"
		}
		return fmt.Errorf("compatible provider request failed: %s", diagnostic.Outcome)
	}
	defer resp.Body.Close()
	diagnostic.HTTP = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		diagnostic.Outcome = "http_error"
		return fmt.Errorf("compatible provider HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 2<<20)).Decode(out); err != nil {
		return errors.New("compatible provider returned invalid or oversized JSON")
	}
	return nil
}

func (p *compatibleProvider) Models(ctx context.Context, key string) (_ []string, err error) {
	diagnostic := newProviderDiagnostic("models")
	defer diagnostic.finish(&err)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, errors.New("invalid provider URL")
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := p.request(req, key, &out, diagnostic); err != nil {
		return nil, err
	}
	models := []string{}
	for _, item := range out.Data {
		if item.ID != "" {
			models = append(models, item.ID)
		}
	}
	sort.Strings(models)
	return models, nil
}

func (p *compatibleProvider) complete(ctx context.Context, key string, body map[string]interface{}) (_ providerReply, _ json.RawMessage, err error) {
	operation := "chat"
	if _, ok := body["max_tokens"]; ok {
		operation = "connection_test"
	}
	diagnostic := newProviderDiagnostic(operation)
	defer diagnostic.finish(&err)
	req, err := jsonRequest(ctx, http.MethodPost, p.baseURL+"/chat/completions", body)
	if err != nil {
		return providerReply{}, nil, errors.New("invalid provider request")
	}
	var out struct {
		Error json.RawMessage `json:"error"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
			Details    struct {
				Reasoning int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
		Choices []struct {
			Finish  string          `json:"finish_reason"`
			Message json.RawMessage `json:"message"`
		} `json:"choices"`
	}
	if err := p.request(req, key, &out, diagnostic); err != nil {
		return providerReply{}, nil, err
	}
	diagnostic.Choices = len(out.Choices)
	diagnostic.PromptTokens = out.Usage.Prompt
	diagnostic.CompletionTokens = out.Usage.Completion
	diagnostic.ReasoningTokens = out.Usage.Details.Reasoning
	if len(out.Error) > 0 && string(out.Error) != "null" {
		diagnostic.Outcome = "upstream_error"
		return providerReply{}, nil, errors.New("compatible provider reported an error inside a successful HTTP response")
	}
	if len(out.Choices) == 0 {
		diagnostic.Outcome = "no_choices"
		return providerReply{}, nil, errors.New("compatible provider returned no choices")
	}
	diagnostic.Finish = "unknown"
	switch out.Choices[0].Finish {
	case "stop", "length", "tool_calls", "content_filter", "error":
		diagnostic.Finish = out.Choices[0].Finish
	}
	raw := out.Choices[0].Message
	var msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
		Calls   []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil || msg.Role != "assistant" {
		return providerReply{}, nil, errors.New("compatible provider returned invalid assistant message")
	}
	reply := providerReply{Text: msg.Content}
	ids := map[string]bool{}
	for _, call := range msg.Calls {
		var args map[string]interface{}
		if call.ID == "" || ids[call.ID] || call.Type != "function" || call.Function.Name == "" || json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args == nil {
			return providerReply{}, nil, errors.New("compatible provider returned invalid tool call or arguments")
		}
		ids[call.ID] = true
		reply.Calls = append(reply.Calls, normalizedCall{ID: call.ID, Name: call.Function.Name, Arguments: args})
	}
	if strings.TrimSpace(reply.Text) == "" && len(reply.Calls) == 0 {
		diagnostic.Outcome = "empty_output"
		if diagnostic.Finish == "length" {
			return providerReply{}, nil, errors.New("compatible provider exhausted its token budget before returning text or tools")
		}
		return providerReply{}, nil, fmt.Errorf("compatible provider returned no output (finish_reason=%s)", diagnostic.Finish)
	}
	return reply, raw, nil
}

func (p *compatibleProvider) Reply(ctx context.Context, key, model string, messages []chatMessage, tools []map[string]interface{}, results []toolResult) (providerReply, error) {
	if p.transcript == nil {
		p.transcript = []interface{}{map[string]string{"role": "system", "content": assistantSystemPrompt}}
		for _, m := range messages {
			p.transcript = append(p.transcript, map[string]string{"role": m.Role, "content": m.Content})
		}
	}
	for _, result := range results[p.consumed:] {
		p.transcript = append(p.transcript, map[string]string{"role": "tool", "tool_call_id": result.ID, "content": mustJSON(result.Value)})
	}
	p.consumed = len(results)
	formatted := []interface{}{}
	for _, t := range tools {
		formatted = append(formatted, map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": t["name"], "description": t["description"], "parameters": t["inputSchema"]}})
	}
	body := map[string]interface{}{"model": model, "messages": p.transcript, "stream": false}
	if len(formatted) > 0 {
		body["tools"] = formatted
		body["tool_choice"] = "auto"
	}
	reply, raw, err := p.complete(ctx, key, body)
	if err == nil {
		p.transcript = append(p.transcript, raw)
	}
	return reply, err
}

func (p *compatibleProvider) Test(ctx context.Context, key, model string) error {
	reply, _, err := p.complete(ctx, key, map[string]interface{}{"model": model, "messages": []map[string]string{{"role": "user", "content": "Reply with OK."}}, "stream": false, "max_tokens": 2048})
	if err == nil && len(reply.Calls) > 0 {
		return errors.New("compatible provider returned unexpected tools during connection test")
	}
	return err
}
