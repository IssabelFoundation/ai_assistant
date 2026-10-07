package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type authUserKey struct{}
type principal struct {
	User        string
	Permissions map[string]bool
}
type apiServer struct {
	cfg        config
	store      *store
	pbx        *pbxClient
	secret     []byte
	client     *http.Client
	sem        chan struct{}
	cacheMu    sync.Mutex
	modelCache map[string]cachedModels
}
type cachedModels struct {
	Values  []string
	Expires time.Time
}

func newHTTPServer(cfg config, store *store, pbx *pbxClient) (*http.Server, error) {
	secret, err := readSecret(cfg.WebSecret, 32)
	if err != nil {
		return nil, err
	}
	providerClient := &http.Client{Timeout: cfg.HTTPTimeout, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		allowed := map[string]bool{"api.openai.com": true, "api.anthropic.com": true, "generativelanguage.googleapis.com": true}
		if !allowed[strings.ToLower(req.URL.Hostname())] {
			return fmt.Errorf("provider redirect blocked")
		}
		if len(via) >= 3 {
			return fmt.Errorf("too many provider redirects")
		}
		return nil
	}}
	a := &apiServer{cfg: cfg, store: store, pbx: pbx, secret: secret, client: providerClient, sem: make(chan struct{}, 4), modelCache: map[string]cachedModels{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", a.health)
	mux.Handle("/v1/", a.auth(http.HandlerFunc(a.route)))
	return &http.Server{Addr: cfg.Listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}, nil
}
func (a *apiServer) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "method not allowed")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
func (a *apiServer) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := strings.TrimSpace(r.Header.Get("X-Issabel-User"))
		permissionHeader := strings.TrimSpace(r.Header.Get("X-Issabel-Permissions"))
		stamp := r.Header.Get("X-Issabel-Timestamp")
		signature := r.Header.Get("X-Issabel-Signature")
		unix, err := strconv.ParseInt(stamp, 10, 64)
		if err != nil || user == "" || signature == "" || abs(time.Now().Unix()-unix) > 60 {
			writeError(w, 401, "invalid proxy authentication")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, a.cfg.MaxBodyBytes))
		if err != nil {
			writeError(w, 413, "request too large")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		hash := sha256.Sum256(body)
		canonical := r.Method + "\n" + r.URL.RequestURI() + "\n" + user + "\n" + permissionHeader + "\n" + stamp + "\n" + hex.EncodeToString(hash[:])
		mac := hmac.New(sha256.New, a.secret)
		_, _ = mac.Write([]byte(canonical))
		expected := hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(expected), []byte(strings.ToLower(signature))) {
			writeError(w, 401, "invalid proxy authentication")
			return
		}
		permissions := map[string]bool{}
		for _, permission := range strings.Split(permissionHeader, ",") {
			if permission != "" {
				permissions[permission] = true
			}
		}
		ctx := context.WithValue(r.Context(), authUserKey{}, principal{User: user, Permissions: permissions})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
func (a *apiServer) route(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/plan-result":
		a.planResult(w, r)
	case r.URL.Path == "/v1/provider":
		a.provider(w, r)
	case r.URL.Path == "/v1/provider/test":
		a.testProvider(w, r)
	case r.URL.Path == "/v1/models":
		a.models(w, r)
	case r.URL.Path == "/v1/conversations":
		a.conversations(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/conversations/"):
		a.conversationByID(w, r, strings.TrimPrefix(r.URL.Path, "/v1/conversations/"))
	case r.URL.Path == "/v1/chat":
		a.chat(w, r, false)
	case r.URL.Path == "/v1/chat/stream":
		a.chat(w, r, true)
	default:
		writeError(w, 404, "not found")
	}
}
func requestPrincipal(r *http.Request) principal { return r.Context().Value(authUserKey{}).(principal) }
func requestUser(r *http.Request) string         { return requestPrincipal(r).User }
func requirePermission(w http.ResponseWriter, r *http.Request, permission string) bool {
	if !requestPrincipal(r).Permissions[permission] {
		writeError(w, 403, "missing permission: "+permission)
		return false
	}
	return true
}
func (a *apiServer) provider(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, "assistant.provider.configure") {
		return
	}
	user := requestUser(r)
	switch r.Method {
	case http.MethodGet:
		view, err := a.store.providerView(user)
		if os.IsNotExist(err) {
			writeJSON(w, 200, providerView{})
			return
		}
		if err != nil {
			writeError(w, 500, "provider configuration unavailable")
			return
		}
		writeJSON(w, 200, view)
	case http.MethodPut:
		var input providerConfig
		if !decodeJSON(w, r, &input) {
			return
		}
		input.Model = strings.TrimSpace(input.Model)
		if input.Provider == "openai_compatible" {
			var err error
			input.BaseURL, err = normalizeBaseURL(input.BaseURL)
			if err != nil {
				writeError(w, 422, err.Error())
				return
			}
		} else {
			input.BaseURL = ""
		}
		if _, err := providerFor(input, a.client); err != nil {
			writeError(w, 422, err.Error())
			return
		}
		if strings.TrimSpace(input.Model) == "" || len(input.Model) > 200 || len(input.APIKey) < 8 || len(input.APIKey) > 1000 {
			writeError(w, 422, "model and a valid API key are required")
			return
		}
		if err := a.store.saveProvider(user, input); err != nil {
			writeError(w, 500, "configuration could not be saved")
			return
		}
		a.invalidateModels(user)
		view, _ := a.store.providerView(user)
		writeJSON(w, 200, view)
	case http.MethodDelete:
		if err := a.store.deleteProvider(user); err != nil {
			writeError(w, 500, "configuration could not be deleted")
			return
		}
		a.invalidateModels(user)
		w.WriteHeader(204)
	default:
		writeError(w, 405, "method not allowed")
	}
}
func (a *apiServer) testProvider(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, "assistant.provider.configure") {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed")
		return
	}
	pc, err := a.store.loadProvider(requestUser(r))
	if err != nil {
		writeError(w, 409, "configure a provider first")
		return
	}
	p, err := providerFor(pc, a.client)
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.HTTPTimeout)
	defer cancel()
	if compatible, ok := p.(*compatibleProvider); ok {
		if err := compatible.Test(ctx, pc.APIKey, pc.Model); err != nil {
			writeError(w, 502, err.Error())
			return
		}
		writeJSON(w, 200, map[string]interface{}{"status": "ok", "model": pc.Model})
		return
	}
	models, err := p.Models(ctx, pc.APIKey)
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"status": "ok", "models_found": len(models)})
}
func (a *apiServer) models(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, "assistant.provider.configure") {
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, 405, "method not allowed")
		return
	}
	user := requestUser(r)
	pc, err := a.store.loadProvider(user)
	if err != nil {
		writeError(w, 409, "configure a provider first")
		return
	}
	fingerprint := sha256.Sum256([]byte(mustJSON(pc)))
	cacheKey := user + ":" + hex.EncodeToString(fingerprint[:])
	a.cacheMu.Lock()
	cached, ok := a.modelCache[cacheKey]
	a.cacheMu.Unlock()
	if ok && time.Now().Before(cached.Expires) {
		writeJSON(w, 200, map[string]interface{}{"models": cached.Values})
		return
	}
	p, err := providerFor(pc, a.client)
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	models, err := p.Models(r.Context(), pc.APIKey)
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	a.cacheMu.Lock()
	a.modelCache[cacheKey] = cachedModels{Values: models, Expires: time.Now().Add(5 * time.Minute)}
	a.cacheMu.Unlock()
	writeJSON(w, 200, map[string]interface{}{"models": models})
}
func (a *apiServer) conversations(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, "assistant.access") {
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, 405, "method not allowed")
		return
	}
	items, err := a.store.listConversations(requestUser(r))
	if err != nil {
		writeError(w, 500, "history unavailable")
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UpdatedAt > items[j].UpdatedAt })
	writeJSON(w, 200, map[string]interface{}{"conversations": items})
}
func (a *apiServer) conversationByID(w http.ResponseWriter, r *http.Request, id string) {
	if !requirePermission(w, r, "assistant.access") {
		return
	}
	user := requestUser(r)
	switch r.Method {
	case http.MethodGet:
		c, err := a.store.loadConversation(user, id)
		if err != nil {
			writeError(w, 404, "conversation not found")
			return
		}
		writeJSON(w, 200, c)
	case http.MethodDelete:
		if err := a.store.deleteConversation(user, id); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		w.WriteHeader(204)
	default:
		writeError(w, 405, "method not allowed")
	}
}

type chatInput struct {
	ConversationID string `json:"conversation_id"`
	Message        string `json:"message"`
}

func (a *apiServer) chat(w http.ResponseWriter, r *http.Request, stream bool) {
	if !requirePermission(w, r, "assistant.plans.create") {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed")
		return
	}
	var input chatInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Message = strings.TrimSpace(input.Message)
	if input.Message == "" || len(input.Message) > 12000 {
		writeError(w, 422, "message must contain 1 to 12000 characters")
		return
	}
	select {
	case a.sem <- struct{}{}:
		defer func() { <-a.sem }()
	default:
		writeError(w, 429, "assistant concurrency limit reached")
		return
	}
	user := requestUser(r)
	var c conversation
	var err error
	if input.ConversationID != "" {
		c, err = a.store.loadConversation(user, input.ConversationID)
		if err != nil {
			writeError(w, 404, "conversation not found")
			return
		}
	} else {
		now := time.Now().Unix()
		c = conversation{ID: randomHex(16), Title: truncate(input.Message, 80), CreatedAt: now, UpdatedAt: now}
	}
	c.Messages = append(c.Messages, chatMessage{Role: "user", Content: input.Message, CreatedAt: time.Now().Unix()})
	c.Messages = boundedMessages(c.Messages, 100, 100000)
	if ambiguousNaturalRange(input.Message) {
		text := "La cantidad no coincide con el rango inclusivo. Indica una cantidad con número inicial, o una lista/rango exacto. / The count does not match the inclusive range; please choose one interpretation."
		c.Messages = append(c.Messages, chatMessage{Role: "assistant", Content: text, CreatedAt: time.Now().Unix()})
		c.UpdatedAt = time.Now().Unix()
		if err := a.store.saveConversation(user, c); err != nil {
			writeError(w, 500, "conversation could not be saved")
			return
		}
		result := map[string]interface{}{"conversation_id": c.ID, "message": text, "plans": []interface{}{}}
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-store")
			fmt.Fprintf(w, "event: complete\ndata: %s\n\n", mustJSON(result))
			return
		}
		writeJSON(w, 200, result)
		return
	}
	pc, err := a.store.loadProvider(user)
	if err != nil {
		writeError(w, 409, "configure a provider first")
		return
	}
	p, err := providerFor(pc, a.client)
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(w, "event: status\ndata: {\"status\":\"thinking\"}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	text, plans, err := runAssistant(r.Context(), p, pc, c.Messages, a.pbx, user, requestPrincipal(r).Permissions)
	if err != nil {
		if stream {
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", mustJSON(map[string]string{"error": err.Error()}))
			return
		}
		writeError(w, 502, err.Error())
		return
	}
	for _, value := range plans {
		if plan, ok := value.(map[string]interface{}); ok {
			if id, ok := plan["plan_id"].(string); ok {
				c.PlanIDs = append(c.PlanIDs, id)
			}
		}
	}
	c.Messages = append(c.Messages, chatMessage{Role: "assistant", Content: text, CreatedAt: time.Now().Unix()})
	c.UpdatedAt = time.Now().Unix()
	if err = a.store.saveConversation(user, c); err != nil {
		if !stream {
			writeError(w, 500, "conversation could not be saved")
		}
		return
	}
	result := map[string]interface{}{"conversation_id": c.ID, "message": text, "plans": plans}
	if stream {
		for _, chunk := range textChunks(text, 48) {
			fmt.Fprintf(w, "event: delta\ndata: %s\n\n", mustJSON(map[string]string{"text": chunk}))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		fmt.Fprintf(w, "event: complete\ndata: %s\n\n", mustJSON(result))
		return
	}
	writeJSON(w, 200, result)
}
func decodeJSON(w http.ResponseWriter, r *http.Request, dest interface{}) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		writeError(w, 400, "invalid JSON request")
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]interface{}{"status": "error", "detail": truncate(message, 400)})
}
func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func boundedMessages(messages []chatMessage, maxMessages, maxBytes int) []chatMessage {
	if len(messages) > maxMessages {
		messages = messages[len(messages)-maxMessages:]
	}
	total := 0
	start := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		size := len(messages[i].Content)
		if total+size > maxBytes {
			break
		}
		total += size
		start = i
	}
	return messages[start:]
}

var naturalRangePattern = regexp.MustCompile(`(?i)([0-9]{1,3})\s+(?:extensiones|extensions).*?(?:desde|from)\s+(?:(?:la|el)\s+)?([0-9]{1,8})\s+(?:a|hasta|to|-)\s+(?:(?:la|el)\s+)?([0-9]{1,8})`)
var naturalBetweenPattern = regexp.MustCompile(`(?i)([0-9]{1,3})\s+(?:extensiones|extensions).*?(?:entre|between)\s+([0-9]{1,8})\s+(?:y|and)\s+([0-9]{1,8})`)

func ambiguousNaturalRange(message string) bool {
	match := naturalRangePattern.FindStringSubmatch(message)
	if len(match) != 4 {
		match = naturalBetweenPattern.FindStringSubmatch(message)
	}
	if len(match) != 4 {
		return false
	}
	count, _ := strconv.Atoi(match[1])
	start, _ := strconv.Atoi(match[2])
	end, _ := strconv.Atoi(match[3])
	return end-start+1 != count
}

func textChunks(value string, size int) []string {
	runes := []rune(value)
	result := []string{}
	for start := 0; start < len(runes); start += size {
		end := start + size
		if end > len(runes) {
			end = len(runes)
		}
		result = append(result, string(runes[start:end]))
	}
	return result
}

func (a *apiServer) invalidateModels(user string) {
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	for key := range a.modelCache {
		if strings.HasPrefix(key, user+":") {
			delete(a.modelCache, key)
		}
	}
}
