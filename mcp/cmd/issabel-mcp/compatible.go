package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
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

func (p *compatibleProvider) request(req *http.Request, key string, out interface{}) error {
	req.Header.Set("Authorization", "Bearer "+key)
	// Do not expose upstream bodies or transport errors: either may echo secrets.
	resp, err := p.client.Do(req)
	if err != nil {
		return errors.New("compatible provider request failed (connection or timeout)")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("compatible provider HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 2<<20)).Decode(out); err != nil {
		return errors.New("compatible provider returned invalid or oversized JSON")
	}
	return nil
}

func (p *compatibleProvider) Models(ctx context.Context, key string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, errors.New("invalid provider URL")
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := p.request(req, key, &out); err != nil {
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

func (p *compatibleProvider) complete(ctx context.Context, key string, body map[string]interface{}) (providerReply, json.RawMessage, error) {
	req, err := jsonRequest(ctx, http.MethodPost, p.baseURL+"/chat/completions", body)
	if err != nil {
		return providerReply{}, nil, errors.New("invalid provider request")
	}
	var out struct {
		Choices []struct {
			Message json.RawMessage `json:"message"`
		} `json:"choices"`
	}
	if err := p.request(req, key, &out); err != nil {
		return providerReply{}, nil, err
	}
	if len(out.Choices) == 0 {
		return providerReply{}, nil, errors.New("compatible provider returned no output")
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
		return providerReply{}, nil, errors.New("compatible provider returned no output")
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
	reply, _, err := p.complete(ctx, key, map[string]interface{}{"model": model, "messages": []map[string]string{{"role": "user", "content": "Reply with OK."}}, "stream": false, "max_tokens": 16})
	if err == nil && len(reply.Calls) > 0 {
		return errors.New("compatible provider returned unexpected tools during connection test")
	}
	return err
}
