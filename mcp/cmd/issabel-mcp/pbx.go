package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type pbxClient struct {
	baseURL string
	key     *rsa.PrivateKey
	http    *http.Client
}

func newPBXClient(cfg config) (*pbxClient, error) {
	parsed, err := url.Parse(cfg.PBXAPIURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("ISSABEL_PBXAPI_URL must be an http(s) URL")
	}
	transport, err := newPBXTransport(cfg, parsed)
	if err != nil {
		return nil, err
	}
	pemData, err := os.ReadFile(cfg.PrivateKey)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errors.New("invalid MCP private key PEM")
	}
	var key *rsa.PrivateKey
	if parsedKey, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes); parseErr == nil {
		var ok bool
		key, ok = parsedKey.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("MCP private key is not RSA")
		}
	} else {
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
	}
	return &pbxClient{baseURL: strings.TrimRight(cfg.PBXAPIURL, "/"), key: key, http: &http.Client{Timeout: cfg.HTTPTimeout, Transport: transport}}, nil
}

func newPBXTransport(cfg config, parsed *url.URL) (*http.Transport, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.PBXAPICAFile != "" {
		caData, err := os.ReadFile(cfg.PBXAPICAFile)
		if err != nil {
			return nil, fmt.Errorf("PBX API CA file: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(caData) {
			return nil, errors.New("PBX API CA file contains no valid certificates")
		}
		tlsConfig.RootCAs = roots
	}
	if cfg.PBXAPITLSInsecure {
		host := strings.ToLower(parsed.Hostname())
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, errors.New("ISSABEL_PBXAPI_TLS_INSECURE is allowed only for a loopback PBX API URL")
		}
		// The connection never leaves this host; remote provider TLS remains strict.
		tlsConfig.InsecureSkipVerify = true
	}
	return &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: tlsConfig}, nil
}

func (p *pbxClient) token(subject string, scopes []string) (string, error) {
	now := time.Now().Unix()
	claims := map[string]interface{}{
		"iss": "issabel-mcp", "aud": "pbxapi", "sub": subject,
		"iat": now, "exp": now + 120, "jti": randomHex(16), "scope": scopes,
	}
	header := map[string]interface{}{"alg": "RS256", "typ": "JWT"}
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	unsigned := rawURL(h) + "." + rawURL(c)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func rawURL(value []byte) string { return base64.RawURLEncoding.EncodeToString(value) }

func randomHex(size int) string {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", value)
}

func (p *pbxClient) call(ctx context.Context, subject, method, path string, input interface{}, scopes []string) (interface{}, error) {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	token, err := p.token(subject, scopes)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("PBX API request to %s failed: %w", p.baseURL, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var result interface{}
	if len(data) != 0 && json.Unmarshal(data, &result) != nil {
		result = string(data)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, pbxAPIError(resp.StatusCode, result)
	}
	return redact(result), nil
}

func pbxAPIError(status int, result interface{}) error {
	if object, ok := result.(map[string]interface{}); ok {
		detail, _ := object["detail"].(string)
		requestID, _ := object["request_id"].(string)
		if detail != "" && requestID != "" {
			return fmt.Errorf("PBX API HTTP %d request_id=%s: %s", status, requestID, truncate(detail, 500))
		}
		if detail != "" {
			return fmt.Errorf("PBX API HTTP %d: %s", status, truncate(detail, 500))
		}
	}
	return fmt.Errorf("PBX API HTTP %d", status)
}

func (p *pbxClient) tool(ctx context.Context, subject, name string, args map[string]interface{}) (interface{}, error) {
	switch name {
	case "list_extensions":
		return p.call(ctx, subject, http.MethodGet, "/mcpextensions", nil, []string{"extensions:read"})
	case "get_extension":
		ext, err := requiredString(args, "extension")
		if err != nil {
			return nil, err
		}
		return p.call(ctx, subject, http.MethodGet, "/mcpextensions/"+url.PathEscape(ext), nil, []string{"extensions:read"})
	case "list_queues":
		return p.call(ctx, subject, http.MethodGet, "/mcpqueues", nil, []string{"queues:read"})
	case "list_numbers":
		return p.call(ctx, subject, http.MethodGet, "/mcpnamespace/numbers", nil, []string{"namespace:read"})
	case "list_destinations":
		return p.call(ctx, subject, http.MethodGet, "/mcpnamespace/destinations", nil, []string{"namespace:read"})
	case "check_number":
		number, err := requiredString(args, "extension")
		if err != nil {
			return nil, err
		}
		number = strings.TrimSpace(number)
		if !digitsOnly(number, 8) {
			return nil, fmt.Errorf("extension must contain 1 to 8 digits")
		}
		return p.call(ctx, subject, http.MethodGet, "/mcpnamespace/"+url.PathEscape(number), nil, []string{"namespace:read"})
	case "check_numbers":
		numbers, err := requiredStringSlice(args, "numbers")
		if err != nil {
			return nil, err
		}
		if len(numbers) > 100 {
			return nil, fmt.Errorf("at most 100 numbers can be checked at once")
		}
		for _, number := range numbers {
			if !digitsOnly(strings.TrimSpace(number), 8) {
				return nil, fmt.Errorf("every number must contain 1 to 8 digits")
			}
		}
		// Digits and separators only, so the path segment needs no escaping.
		return p.call(ctx, subject, http.MethodGet, "/mcpnamespace/"+strings.Join(numbers, ","), nil, []string{"namespace:read"})
	case "create_extension_plan":
		body := cloneMap(args)
		if err := normalizeExtensionSelector(body); err != nil {
			return nil, err
		}
		body["operation"] = "create"
		return p.call(ctx, subject, http.MethodPost, "/mcpplans", body, []string{"extensions:plan"})
	case "update_extension_plan":
		body := cloneMap(args)
		if err := normalizeExtensionSelector(body); err != nil {
			return nil, err
		}
		body["operation"] = "update"
		return p.call(ctx, subject, http.MethodPost, "/mcpplans", body, []string{"extensions:plan"})
	case "delete_extension_plan":
		body := cloneMap(args)
		if err := normalizeExtensionSelector(body); err != nil {
			return nil, err
		}
		body["operation"] = "delete"
		return p.call(ctx, subject, http.MethodPost, "/mcpplans", body, []string{"extensions:plan"})
	case "create_queue_plan":
		body := cloneMap(args)
		if err := normalizeQueueArguments(body); err != nil {
			return nil, err
		}
		body["operation"] = "create_queue"
		return p.call(ctx, subject, http.MethodPost, "/mcpplans", body, []string{"queues:plan"})
	case "delete_queue_plan":
		extension, err := requiredString(args, "extension")
		if err != nil {
			return nil, err
		}
		body := map[string]interface{}{"operation": "delete_queue", "extension": extension}
		return p.call(ctx, subject, http.MethodPost, "/mcpplans", body, []string{"queues:plan"})
	case "list_ringgroups":
		return p.call(ctx, subject, http.MethodGet, "/mcpringgroups", nil, []string{"ringgroups:read"})
	case "list_timegroups":
		return p.call(ctx, subject, http.MethodGet, "/mcptimegroups", nil, []string{"time:read"})
	case "list_timeconditions":
		return p.call(ctx, subject, http.MethodGet, "/mcptimeconditions", nil, []string{"time:read"})
	case "list_ivrs":
		return p.call(ctx, subject, http.MethodGet, "/mcpivrs", nil, []string{"ivr:read"})
	case "create_ivr_plan":
		body := cloneMap(args)
		body["operation"] = "create_ivr"
		return p.call(ctx, subject, http.MethodPost, "/mcpplans", body, []string{"ivr:plan"})
	case "update_ivr_plan":
		body := cloneMap(args)
		body["operation"] = "update_ivr"
		return p.call(ctx, subject, http.MethodPost, "/mcpplans", body, []string{"ivr:plan"})
	case "delete_ivr_plan":
		id, err := requiredString(args, "id")
		if err != nil {
			return nil, err
		}
		body := map[string]interface{}{"operation": "delete_ivr", "id": id}
		return p.call(ctx, subject, http.MethodPost, "/mcpplans", body, []string{"ivr:plan"})
	case "create_time_group_plan":
		body := cloneMap(args)
		body["operation"] = "create_time_group"
		return p.call(ctx, subject, http.MethodPost, "/mcpplans", body, []string{"time:plan"})
	case "delete_time_group_plan":
		id, err := requiredString(args, "id")
		if err != nil {
			return nil, err
		}
		body := map[string]interface{}{"operation": "delete_time_group", "id": id}
		return p.call(ctx, subject, http.MethodPost, "/mcpplans", body, []string{"time:plan"})
	case "create_time_condition_plan":
		body := cloneMap(args)
		body["operation"] = "create_time_condition"
		return p.call(ctx, subject, http.MethodPost, "/mcpplans", body, []string{"time:plan"})
	case "delete_time_condition_plan":
		id, err := requiredString(args, "id")
		if err != nil {
			return nil, err
		}
		body := map[string]interface{}{"operation": "delete_time_condition", "id": id}
		return p.call(ctx, subject, http.MethodPost, "/mcpplans", body, []string{"time:plan"})
	case "create_ring_group_plan":
		body := cloneMap(args)
		body["operation"] = "create_ringgroup"
		return p.call(ctx, subject, http.MethodPost, "/mcpplans", body, []string{"ringgroups:plan"})
	case "update_ring_group_plan":
		body := cloneMap(args)
		body["operation"] = "update_ringgroup"
		return p.call(ctx, subject, http.MethodPost, "/mcpplans", body, []string{"ringgroups:plan"})
	case "delete_ring_group_plan":
		extension, err := requiredString(args, "extension")
		if err != nil {
			return nil, err
		}
		body := map[string]interface{}{"operation": "delete_ringgroup", "extension": extension}
		return p.call(ctx, subject, http.MethodPost, "/mcpplans", body, []string{"ringgroups:plan"})
	case "get_plan_status":
		id, err := requiredString(args, "plan_id")
		if err != nil {
			return nil, err
		}
		return p.call(ctx, subject, http.MethodGet, "/mcpplans/"+url.PathEscape(id), nil, []string{"plans:read"})
	case "cancel_plan":
		id, err := requiredString(args, "plan_id")
		if err != nil {
			return nil, err
		}
		return p.call(ctx, subject, http.MethodDelete, "/mcpplans/"+url.PathEscape(id), nil, []string{"plans:cancel"})
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

// normalizeQueueArguments treats failover.type as a discriminator. Provider
// tool calls sometimes populate optional fields with empty values; those
// fields must not make an otherwise valid hangup failover ambiguous.
func normalizeQueueArguments(args map[string]interface{}) error {
	rawFailover, exists := args["failover"]
	if !exists {
		return errors.New("failover is required")
	}
	failover, ok := rawFailover.(map[string]interface{})
	if !ok {
		return errors.New("failover must be an object")
	}
	failoverCopy := make(map[string]interface{}, len(failover))
	for key, value := range failover {
		failoverCopy[key] = value
	}
	failover = failoverCopy
	args["failover"] = failover
	failoverType, ok := failover["type"].(string)
	if !ok {
		return errors.New("failover.type is required")
	}
	switch failoverType {
	case "hangup":
		delete(failover, "destination_extension")
		return nil
	case "extension", "queue", "ring_group":
		destination, ok := failover["destination_extension"].(string)
		if !ok || destination == "" {
			return errors.New("failover.destination_extension is required unless failover.type is hangup")
		}
		for _, char := range destination {
			if char < '0' || char > '9' {
				return errors.New("failover.destination_extension must contain 1 to 8 digits")
			}
		}
		if len(destination) > 8 {
			return errors.New("failover.destination_extension must contain 1 to 8 digits")
		}
		return nil
	default:
		return errors.New("failover.type must be hangup, extension, queue, or ring_group")
	}
}

// normalizeExtensionSelector translates the discriminated selector advertised
// to LLMs into the flat PBX API contract. The selected mode is authoritative,
// so stray fields from the other mode can never create an ambiguous request.
// The previous flat contract remains accepted for external MCP clients.
func normalizeExtensionSelector(args map[string]interface{}) error {
	if rawSelector, exists := args["selector"]; exists {
		selector, ok := rawSelector.(map[string]interface{})
		if !ok {
			return errors.New("selector must be an object with mode list or range")
		}
		mode, ok := selector["mode"].(string)
		if !ok {
			return errors.New("selector.mode must be list or range")
		}
		delete(args, "selector")
		delete(args, "extensions")
		delete(args, "start_extension")
		delete(args, "count")
		delete(args, "end_extension")
		switch mode {
		case "list":
			extensions, ok := selector["extensions"].([]interface{})
			if !ok || len(extensions) < 1 || len(extensions) > 100 {
				return errors.New("selector.extensions must contain between 1 and 100 extensions when selector.mode is list")
			}
			args["extensions"] = extensions
			return nil
		case "range":
			start, hasStart := integerArgument(selector["start_extension"])
			count, hasCount := integerArgument(selector["count"])
			if !hasStart || start < 0 || start > 99999999 {
				return errors.New("selector.start_extension is required and must contain 1 to 8 digits when selector.mode is range")
			}
			if !hasCount || count < 1 || count > 100 {
				return errors.New("selector.count is required and must be between 1 and 100 when selector.mode is range")
			}
			if start+count-1 > 99999999 {
				return errors.New("selector range exceeds 8 digits")
			}
			args["start_extension"] = start
			args["count"] = count
			return nil
		default:
			return errors.New("selector.mode must be list or range")
		}
	}

	// Backward compatibility: an empty legacy list is not a selector.
	if extensions, exists := args["extensions"].([]interface{}); exists && len(extensions) == 0 {
		delete(args, "extensions")
	}
	canonicalizeEquivalentExtensionSelector(args)
	_, hasList := args["extensions"]
	hasRange := args["start_extension"] != nil || args["count"] != nil || args["end_extension"] != nil
	if hasList && hasRange {
		return errors.New("ambiguous legacy selector: use extensions or start_extension/count, not both")
	}
	if !hasList && !hasRange {
		return errors.New("selector is required")
	}
	return nil
}

// canonicalizeEquivalentExtensionSelector removes a redundant range only when
// it denotes exactly the same set as the explicit extension list. Conflicting
// selectors remain untouched so the PBX API rejects and audits the ambiguity.
func canonicalizeEquivalentExtensionSelector(args map[string]interface{}) {
	rawList, hasList := args["extensions"].([]interface{})
	start, hasStart := integerArgument(args["start_extension"])
	count, hasCount := integerArgument(args["count"])
	if !hasList || !hasStart || !hasCount || count < 1 || len(rawList) != count {
		return
	}
	explicit := make(map[string]bool, len(rawList))
	for _, value := range rawList {
		var extension string
		switch typed := value.(type) {
		case string:
			extension = typed
		case float64:
			if typed != float64(int64(typed)) {
				return
			}
			extension = fmt.Sprintf("%d", int64(typed))
		default:
			return
		}
		explicit[extension] = true
	}
	if len(explicit) != count {
		return
	}
	for offset := 0; offset < count; offset++ {
		if !explicit[fmt.Sprintf("%d", start+offset)] {
			return
		}
	}
	if end, hasEnd := integerArgument(args["end_extension"]); hasEnd && end != start+count-1 {
		return
	}
	delete(args, "start_extension")
	delete(args, "count")
	delete(args, "end_extension")
}

func integerArgument(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case float64:
		if typed != float64(int64(typed)) {
			return 0, false
		}
		return int(typed), true
	case int:
		return typed, true
	case string:
		n, err := strconv.Atoi(typed)
		return n, err == nil
	default:
		return 0, false
	}
}

func requiredString(args map[string]interface{}, key string) (string, error) {
	value, ok := args[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

// digitsOnly rejects anything a PBX number cannot contain, so a provider
// supplied value can never reach the API as a path or SQL fragment.
func digitsOnly(value string, maxDigits int) bool {
	if value == "" || len(value) > maxDigits {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// requiredStringSlice reads an array of numbers. Providers sometimes send JSON
// numbers where the schema asks for strings, so both are accepted and
// normalized to digits-only decimal text.
func requiredStringSlice(args map[string]interface{}, key string) ([]string, error) {
	raw, ok := args[key]
	if !ok {
		return nil, fmt.Errorf("%s is required", key)
	}
	items, ok := raw.([]interface{})
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("%s must be a non-empty array", key)
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		switch typed := item.(type) {
		case string:
			text := strings.TrimSpace(typed)
			if text == "" {
				return nil, fmt.Errorf("%s must contain non-empty values", key)
			}
			values = append(values, text)
		case float64:
			values = append(values, strconv.FormatFloat(typed, 'f', -1, 64))
		default:
			return nil, fmt.Errorf("%s must contain numbers", key)
		}
	}
	return values, nil
}

func cloneMap(source map[string]interface{}) map[string]interface{} {
	dest := make(map[string]interface{}, len(source)+1)
	for key, value := range source {
		dest[key] = value
	}
	return dest
}

func redact(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		clean := make(map[string]interface{}, len(typed))
		for key, child := range typed {
			lower := strings.ToLower(key)
			if lower == "secret" || lower == "password" || lower == "pin" || strings.Contains(lower, "api_key") {
				continue
			}
			clean[key] = redact(child)
		}
		return clean
	case []interface{}:
		clean := make([]interface{}, len(typed))
		for i, child := range typed {
			clean[i] = redact(child)
		}
		return clean
	default:
		return value
	}
}
