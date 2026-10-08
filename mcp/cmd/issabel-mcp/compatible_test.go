package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompatibleBaseURL(t *testing.T) {
	for _, value := range []string{"", "http://example.org/v1", "https:///v1", "https://key@example.org", "https://example.org?key=x", "https://example.org?", "https://example.org#", "https://example.org/v1/chat/completions"} {
		if _, err := normalizeBaseURL(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	if got, err := normalizeBaseURL(" https://openrouter.ai/api/v1/ "); err != nil || got != "https://openrouter.ai/api/v1" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestCompatibleTranscript(t *testing.T) {
	round := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://example.org/v1/chat/completions" || req.Header.Get("Authorization") != "Bearer secret-key" {
			t.Fatal("incorrect URL or authentication")
		}
		var body struct {
			Model    string                   `json:"model"`
			Messages []map[string]interface{} `json:"messages"`
			Tools    []map[string]interface{} `json:"tools"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "vendor/model" || len(body.Tools) != 1 {
			t.Fatal("missing model or tools")
		}
		expected := []int{2, 5, 7}[round]
		if len(body.Messages) != expected {
			t.Fatalf("round %d: %#v", round, body.Messages)
		}
		if round > 0 {
			if body.Messages[2]["reasoning_details"] == nil || body.Messages[3]["tool_call_id"] != "a" || body.Messages[4]["tool_call_id"] != "b" {
				t.Fatal("lost raw message or tool order")
			}
		}
		if round == 2 && body.Messages[6]["tool_call_id"] != "c" {
			t.Fatal("lost second round")
		}
		replies := []string{
			`{"choices":[{"message":{"role":"assistant","content":null,"reasoning_details":[{"text":"opaque"}],"tool_calls":[{"id":"a","type":"function","function":{"name":"check_number","arguments":"{\"number\":100}"}},{"id":"b","type":"function","function":{"name":"check_number","arguments":"{\"number\":101}"}}]}}]}`,
			`{"choices":[{"message":{"role":"assistant","content":"Checking again","tool_calls":[{"id":"c","type":"function","function":{"name":"check_number","arguments":"{\"number\":102}"}}]}}]}`,
			`{"choices":[{"message":{"role":"assistant","content":"Done"}}]}`,
		}
		response := replies[round]
		round++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header), Request: req}, nil
	})}
	p, err := newCompatibleProvider("https://example.org/v1", client)
	if err != nil {
		t.Fatal(err)
	}
	messages := []chatMessage{{Role: "user", Content: "Check numbers"}}
	tools := []map[string]interface{}{{"name": "check_number", "description": "Check", "inputSchema": map[string]interface{}{"type": "object"}}}
	var results []toolResult
	for i := 0; i < 3; i++ {
		reply, err := p.Reply(context.Background(), "secret-key", "vendor/model", messages, tools, results)
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range reply.Calls {
			results = append(results, toolResult{ID: call.ID, Value: map[string]bool{"available": true}})
		}
		if i == 2 && reply.Text != "Done" {
			t.Fatal(reply)
		}
	}
	other, _ := newCompatibleProvider("https://example.org/v1", client)
	if other.transcript != nil {
		t.Fatal("transcript shared across requests")
	}
}

func TestCompatibleErrors(t *testing.T) {
	for _, body := range []string{`{`, `{"choices":[]}`, `{"choices":[{"message":{"role":"assistant","content":""}}]}`, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"x","arguments":"[]"}}]}}]}`} {
		p, _ := newCompatibleProvider("https://example.org", jsonClient(body))
		if _, err := p.Reply(context.Background(), "secret-key", "model", nil, nil, nil); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
	for _, status := range []int{401, 404, 429, 500} {
		p, _ := newCompatibleProvider("https://example.org", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"secret-key"}}`)), Header: make(http.Header)}, nil
		})})
		err := p.Test(context.Background(), "secret-key", "missing-model")
		if err == nil || !strings.Contains(err.Error(), fmt.Sprint(status)) || strings.Contains(err.Error(), "secret-key") {
			t.Fatal(err)
		}
	}
}

func TestCompatibleModelsAndProbe(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"data":[{"id":"z/model"},{"id":"a/model"}]}`
		if req.URL.Path == "/api/v1/chat/completions" {
			var input map[string]interface{}
			json.NewDecoder(req.Body).Decode(&input)
			if input["max_tokens"] != float64(2048) || input["tools"] != nil || input["model"] != "vendor/model" || strings.Contains(mustJSON(input), "PBX") {
				t.Fatal(input)
			}
			body = `{"choices":[{"message":{"role":"assistant","content":"OK"}}]}`
		} else if req.URL.Path != "/api/v1/models" {
			t.Fatal(req.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	p, _ := newCompatibleProvider("https://example.org/api/v1", client)
	models, err := p.Models(context.Background(), "secret-key")
	if err != nil || strings.Join(models, ",") != "a/model,z/model" {
		t.Fatalf("%v %v", models, err)
	}
	if err := p.Test(context.Background(), "secret-key", "vendor/model"); err != nil {
		t.Fatal(err)
	}
	if p.transcript != nil {
		t.Fatal("probe changed transcript")
	}
}

func TestCompatibleBlocksRedirects(t *testing.T) {
	hits := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		hits++
		return &http.Response{StatusCode: 307, Header: http.Header{"Location": []string{"https://other.example/models"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	})}
	p, _ := newCompatibleProvider("https://example.org", client)
	if _, err := p.Models(context.Background(), "secret-key"); err == nil {
		t.Fatal("redirect accepted")
	}
	if hits != 1 || client.CheckRedirect != nil {
		t.Fatalf("followed redirect or changed original client: %d", hits)
	}
}

func TestInvalidateModels(t *testing.T) {
	a := apiServer{modelCache: map[string]cachedModels{"alice:old": {Expires: time.Now()}, "bob:old": {Expires: time.Now()}}}
	a.invalidateModels("alice")
	if len(a.modelCache) != 1 {
		t.Fatal(a.modelCache)
	}
	if _, ok := a.modelCache["bob:old"]; !ok {
		t.Fatal("invalidated another user")
	}
}

func TestCompatibleProviderHandlers(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte("01234567890123456789012345678901"), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := newStore(config{MasterKey: keyPath, StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	a := apiServer{store: st, cfg: config{HTTPTimeout: time.Second}, modelCache: map[string]cachedModels{"alice:old": {}}, client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		status := 200
		body := `{"choices":[{"message":{"role":"assistant","content":"OK"}}]}`
		if strings.HasSuffix(req.URL.Path, "/models") {
			status = 404
			body = `{}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	call := func(handler http.HandlerFunc, method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), authUserKey{}, principal{User: "alice", Permissions: map[string]bool{"assistant.provider.configure": true}}))
		w := httptest.NewRecorder()
		handler(w, req)
		return w
	}
	body := `{"provider":"openai_compatible","base_url":" https://example.org/v1/ ","model":"vendor/model","api_key":"secret-key"}`
	w := call(a.provider, "PUT", body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"base_url":"https://example.org/v1"`) || strings.Contains(w.Body.String(), "secret-key") || len(a.modelCache) != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(a.models, "GET", ""); w.Code != 502 {
		t.Fatal(w.Code)
	}
	if w := call(a.testProvider, "POST", "{}"); w.Code != 200 || !strings.Contains(w.Body.String(), "vendor/model") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(a.provider, "GET", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	for _, bad := range []string{strings.Replace(body, "https://", "http://", 1), strings.Replace(body, "secret-key", "", 1)} {
		if w := call(a.provider, "PUT", bad); w.Code != 422 {
			t.Fatal(w.Code)
		}
	}
	a.modelCache["alice:new"] = cachedModels{}
	if w := call(a.provider, "DELETE", ""); w.Code != 204 || len(a.modelCache) != 0 {
		t.Fatal(w.Code)
	}
}

func TestCompatibleDiagnostics(t *testing.T) {
	var logs bytes.Buffer
	original := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(original)
	for _, tc := range []struct{ body, outcome, message string }{
		{`{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":""}}],"usage":{"completion_tokens":16,"completion_tokens_details":{"reasoning_tokens":16}}}`, "empty_output", "exhausted its token budget"},
		{`{"choices":[],"error":{"message":"secret-key private-conversation"}}`, "upstream_error", "inside a successful HTTP response"},
		{`{"choices":[]}`, "no_choices", "no choices"},
		{`{"choices":[{"finish_reason":"secret-key private-conversation","message":{"role":"assistant","content":""}}]}`, "empty_output", "finish_reason=unknown"},
	} {
		logs.Reset()
		p, _ := newCompatibleProvider("https://example.org", jsonClient(tc.body))
		err := p.Test(context.Background(), "secret-key", "private-model")
		if err == nil || !strings.Contains(err.Error(), tc.message) || !strings.Contains(err.Error(), "diagnostic_id=") {
			t.Fatalf("unexpected error: %v", err)
		}
		id := strings.TrimSuffix(strings.Split(err.Error(), "diagnostic_id=")[1], ")")
		if !strings.Contains(logs.String(), "id="+id) || !strings.Contains(logs.String(), "outcome="+tc.outcome) || !strings.Contains(logs.String(), "operation=connection_test") {
			t.Fatal(logs.String())
		}
		for _, secret := range []string{"secret-key", "private-conversation", "private-model"} {
			if strings.Contains(logs.String()+err.Error(), secret) {
				t.Fatal("diagnostics leaked sensitive content")
			}
		}
	}
}
