package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }
func jsonClient(response string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
	})}
}

func TestProviderKeyIsEncryptedAndIsolated(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "master.key")
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(key)), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := newStore(config{MasterKey: keyPath, StateDir: filepath.Join(dir, "state")})
	if err != nil {
		t.Fatal(err)
	}
	secret := "sk-this-must-never-be-stored-in-plaintext"
	if err := s.saveProvider("alice", providerConfig{Provider: "openai", Model: "gpt-test", APIKey: secret}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.providerPath("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("provider key was stored in plaintext")
	}
	if _, err := s.loadProvider("bob"); !os.IsNotExist(err) {
		t.Fatalf("expected per-user isolation, got %v", err)
	}
	view, err := s.providerView("alice")
	if err != nil {
		t.Fatal(err)
	}
	if !view.HasKey || view.KeySuffix != "text" {
		t.Fatalf("unexpected masked view: %#v", view)
	}
}

func TestToolCatalogCannotApproveOrExecute(t *testing.T) {
	for _, tool := range toolDefinitions() {
		name := tool["name"].(string)
		if strings.Contains(name, "approve") || strings.Contains(name, "execute") || strings.Contains(name, "shell") || strings.Contains(name, "originate") {
			t.Fatalf("unsafe tool exposed: %s", name)
		}
	}
}

func TestPlanToolsRequireDiscriminatedSelector(t *testing.T) {
	for _, definition := range toolDefinitions() {
		name := definition["name"].(string)
		if !isExtensionPlanTool(name) {
			continue
		}
		schema := definition["inputSchema"].(map[string]interface{})
		properties := schema["properties"].(map[string]interface{})
		selector, ok := properties["selector"].(map[string]interface{})
		if !ok {
			t.Fatalf("%s has no selector object", name)
		}
		selectorProperties := selector["properties"].(map[string]interface{})
		mode := selectorProperties["mode"].(map[string]interface{})
		if mode["enum"] == nil || selectorProperties["end_extension"] != nil {
			t.Fatalf("%s selector is not discriminated or exposes redundant end_extension: %#v", name, selector)
		}
		required := schema["required"].([]string)
		found := false
		for _, field := range required {
			if field == "selector" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s does not require selector", name)
		}
	}
}

func TestQueuePlanToolRequiresBothAgentTypes(t *testing.T) {
	for _, definition := range toolDefinitions() {
		if definition["name"] != "create_queue_plan" {
			continue
		}
		schema := definition["inputSchema"].(map[string]interface{})
		properties := schema["properties"].(map[string]interface{})
		agents := properties["agents"].(map[string]interface{})
		required := agents["required"].([]string)
		if len(required) != 2 || required[0] != "static" || required[1] != "dynamic" {
			t.Fatalf("queue agents are not explicit: %#v", agents)
		}
		failover := properties["failover"].(map[string]interface{})
		if failover["additionalProperties"] != false {
			t.Fatalf("queue failover accepts arbitrary destinations: %#v", failover)
		}
		// hangup, extension, queue, ring_group and ivr.
		if len(failover["properties"].(map[string]interface{})["type"].(map[string]interface{})["enum"].([]string)) != 5 {
			t.Fatalf("the destination enum must list five families: %#v", failover)
		}
		return
	}
	t.Fatal("create_queue_plan is not exposed")
}

func TestQueueInventoryAndDeletionToolsAreExposed(t *testing.T) {
	found := map[string]bool{}
	for _, definition := range toolDefinitions() {
		found[definition["name"].(string)] = true
	}
	if !found["list_queues"] || !found["delete_queue_plan"] {
		t.Fatalf("queue management tools are missing: %#v", found)
	}
	if toolAllowed("list_queues", map[string]bool{"assistant.queues.read": true}) != true {
		t.Fatal("queue read permission does not authorize list_queues")
	}
	if toolAllowed("delete_queue_plan", map[string]bool{"assistant.queues.plan": true}) != true {
		t.Fatal("queue plan permission does not authorize delete_queue_plan")
	}
	if toolAllowed("delete_queue_plan", map[string]bool{"assistant.queues.read": true}) {
		t.Fatal("queue read permission authorized deletion planning")
	}
}

func TestNamespaceReadToolsAreExposedAndGated(t *testing.T) {
	found := map[string]bool{}
	for _, definition := range toolDefinitions() {
		found[definition["name"].(string)] = true
	}
	if !found["list_numbers"] || !found["list_destinations"] || !found["check_number"] || !found["check_numbers"] {
		t.Fatalf("namespace read tools are missing: %#v", found)
	}
	namespaceRead := map[string]bool{"assistant.namespace.read": true}
	for _, name := range []string{"list_numbers", "list_destinations", "check_number", "check_numbers"} {
		if !toolAllowed(name, namespaceRead) {
			t.Fatalf("assistant.namespace.read does not authorize %s", name)
		}
	}
	// Reading the namespace must not grant any planning capability.
	if toolAllowed("create_extension_plan", namespaceRead) || toolAllowed("create_queue_plan", namespaceRead) {
		t.Fatal("namespace read permission authorized plan creation")
	}
	// And a plan permission must not grant the namespace read.
	if toolAllowed("list_numbers", map[string]bool{"assistant.plans.create": true}) {
		t.Fatal("plan permission authorized the namespace read")
	}
}

// A not-found extension lookup must never be presented as an availability
// answer; the model has to reach for check_number instead.
func TestAvailabilityIsNotInferredFromExtensionLookup(t *testing.T) {
	descriptions := map[string]string{}
	for _, definition := range toolDefinitions() {
		descriptions[definition["name"].(string)] = definition["description"].(string)
	}
	for _, name := range []string{"list_extensions", "get_extension"} {
		if !strings.Contains(descriptions[name], "check_number") {
			t.Fatalf("%s does not point at check_number: %q", name, descriptions[name])
		}
	}
	if !strings.Contains(descriptions["get_extension"], "does NOT mean the number is free") {
		t.Fatalf("get_extension does not warn about the wrong inference: %q", descriptions["get_extension"])
	}
	if !strings.Contains(descriptions["check_number"], "before creating an extension plan") {
		t.Fatalf("check_number does not instruct when to call it: %q", descriptions["check_number"])
	}
	if !strings.Contains(assistantSystemPrompt, "check_number") {
		t.Fatal("the system prompt does not require verifying numbers")
	}
	// The failure that motivated this: the model answered "yes, you can use 411"
	// while it could not check feature codes. Unverifiable must never become a
	// positive availability claim.
	if !strings.Contains(assistantSystemPrompt, "unconfirmed") ||
		!strings.Contains(assistantSystemPrompt, "absence of evidence") {
		t.Fatal("the system prompt does not forbid claiming availability it could not verify")
	}
	// Answering "which numbers are free between 410 and 415" used to skip
	// verification, because checking six numbers looked too expensive.
	if !strings.Contains(assistantSystemPrompt, "which numbers are free in a range") ||
		!strings.Contains(assistantSystemPrompt, "unverified") {
		t.Fatal("the system prompt does not require verifying availability questions")
	}
	if !strings.Contains(descriptions["check_numbers"], "which numbers are available") {
		t.Fatalf("check_numbers does not cover availability questions: %q", descriptions["check_numbers"])
	}
	if !strings.Contains(descriptions["list_numbers"], "check_numbers") {
		t.Fatalf("list_numbers does not redirect narrow questions: %q", descriptions["list_numbers"])
	}
}

func TestBatchNumberCheckNormalizesProviderInput(t *testing.T) {
	values, err := requiredStringSlice(map[string]interface{}{"numbers": []interface{}{"410", "411"}}, "numbers")
	if err != nil || len(values) != 2 || values[1] != "411" {
		t.Fatalf("string numbers not accepted: %v %v", values, err)
	}
	// Providers sometimes send JSON numbers where the schema asks for strings.
	values, err = requiredStringSlice(map[string]interface{}{"numbers": []interface{}{float64(410), float64(411)}}, "numbers")
	if err != nil || len(values) != 2 || values[0] != "410" {
		t.Fatalf("numeric input not normalized: %v %v", values, err)
	}
	if _, err := requiredStringSlice(map[string]interface{}{}, "numbers"); err == nil {
		t.Fatal("missing numbers accepted")
	}
	if _, err := requiredStringSlice(map[string]interface{}{"numbers": []interface{}{}}, "numbers"); err == nil {
		t.Fatal("empty list accepted")
	}
	if _, err := requiredStringSlice(map[string]interface{}{"numbers": []interface{}{map[string]interface{}{}}}, "numbers"); err == nil {
		t.Fatal("non-scalar entry accepted")
	}
}

func TestRingGroupToolsAreExposedAndGated(t *testing.T) {
	found := map[string]bool{}
	for _, definition := range toolDefinitions() {
		found[definition["name"].(string)] = true
	}
	for _, name := range []string{"list_ringgroups", "create_ring_group_plan", "update_ring_group_plan", "delete_ring_group_plan"} {
		if !found[name] {
			t.Fatalf("ring group tool %s is missing: %#v", name, found)
		}
	}
	if !toolAllowed("list_ringgroups", map[string]bool{"assistant.ringgroups.read": true}) {
		t.Fatal("ring group read permission does not authorize list_ringgroups")
	}
	planPermission := map[string]bool{"assistant.ringgroups.plan": true}
	for _, name := range []string{"create_ring_group_plan", "update_ring_group_plan", "delete_ring_group_plan"} {
		if !toolAllowed(name, planPermission) {
			t.Fatalf("ring group plan permission does not authorize %s", name)
		}
	}
	// Ring group planning must not leak into plan approval, and the queue
	// permissions must not authorize ring group planning.
	if toolAllowed("create_ring_group_plan", map[string]bool{"assistant.queues.plan": true}) {
		t.Fatal("queue plan permission authorized ring group planning")
	}
	if toolAllowed("create_ring_group_plan", map[string]bool{"assistant.ringgroups.read": true}) {
		t.Fatal("ring group read permission authorized planning")
	}
	for _, name := range []string{"create_ring_group_plan", "update_ring_group_plan", "delete_ring_group_plan"} {
		if !isPlanMutationTool(name) {
			t.Fatalf("%s is not audited as a plan mutation", name)
		}
	}
	// The ring group enum must stay in sync with controllers/ringgroups.php.
	create := map[string]interface{}{}
	for _, definition := range toolDefinitions() {
		if definition["name"] == "create_ring_group_plan" {
			create = definition["inputSchema"].(map[string]interface{})
		}
	}
	properties := create["properties"].(map[string]interface{})
	strategy := properties["strategy"].(map[string]interface{})
	if len(strategy["enum"].([]string)) != 8 {
		t.Fatalf("ring group strategies are incomplete: %#v", strategy)
	}
	if properties["failover"] == nil {
		t.Fatal("create_ring_group_plan has no failover")
	}
	// The update tool reuses the same field schemas, but nested under changes,
	// and must reject unknown fields and an empty change set.
	updateSchema := map[string]interface{}{}
	for _, definition := range toolDefinitions() {
		if definition["name"] == "update_ring_group_plan" {
			updateSchema = definition["inputSchema"].(map[string]interface{})
		}
	}
	updateRequired := updateSchema["required"].([]string)
	if len(updateRequired) != 2 || updateRequired[0] != "extension" || updateRequired[1] != "changes" {
		t.Fatalf("update_ring_group_plan required fields: %#v", updateRequired)
	}
	updateProperties := updateSchema["properties"].(map[string]interface{})
	changesSchema := updateProperties["changes"].(map[string]interface{})
	if changesSchema["additionalProperties"] != false || changesSchema["minProperties"] != 1 {
		t.Fatalf("update changes are not constrained: %#v", changesSchema)
	}
	if len(changesSchema["properties"].(map[string]interface{})) != 5 {
		t.Fatalf("update changes fields are incomplete: %#v", changesSchema["properties"])
	}
	if updateProperties["failover"] != nil {
		t.Fatal("failover must only be reachable through changes on update")
	}
	required := create["required"].([]string)
	if len(required) != 5 {
		t.Fatalf("create_ring_group_plan must require five fields: %#v", required)
	}
}

func TestTimeToolsAreExposedAndGated(t *testing.T) {
	found := map[string]bool{}
	for _, definition := range toolDefinitions() {
		found[definition["name"].(string)] = true
	}
	for _, name := range []string{"list_timegroups", "list_timeconditions", "create_time_group_plan",
		"delete_time_group_plan", "create_time_condition_plan", "delete_time_condition_plan"} {
		if !found[name] {
			t.Fatalf("time tool %s is missing: %#v", name, found)
		}
	}
	readPermission := map[string]bool{"assistant.time.read": true}
	for _, name := range []string{"list_timegroups", "list_timeconditions"} {
		if !toolAllowed(name, readPermission) {
			t.Fatalf("time read permission does not authorize %s", name)
		}
	}
	planPermission := map[string]bool{"assistant.time.plan": true}
	for _, name := range []string{"create_time_group_plan", "delete_time_group_plan",
		"create_time_condition_plan", "delete_time_condition_plan"} {
		if !toolAllowed(name, planPermission) {
			t.Fatalf("time plan permission does not authorize %s", name)
		}
		if !isPlanMutationTool(name) {
			t.Fatalf("%s is not audited as a plan mutation", name)
		}
	}
	if toolAllowed("create_time_group_plan", readPermission) {
		t.Fatal("time read permission authorized planning")
	}
	if toolAllowed("list_timegroups", planPermission) {
		t.Fatal("time plan permission authorized reading")
	}
	// The range schema must stay bounded and keep the same weekday enum the
	// server validates against.
	schema := map[string]interface{}{}
	for _, definition := range toolDefinitions() {
		if definition["name"] == "create_time_group_plan" {
			schema = definition["inputSchema"].(map[string]interface{})
		}
	}
	properties := schema["properties"].(map[string]interface{})
	times := properties["times"].(map[string]interface{})
	if times["minItems"] != 1 || times["maxItems"] != 50 {
		t.Fatalf("time ranges are not bounded: %#v", times)
	}
	rangeSchema := times["items"].(map[string]interface{})
	if rangeSchema["additionalProperties"] != false {
		t.Fatalf("a time range accepts unknown fields: %#v", rangeSchema)
	}
	rangeFields := rangeSchema["properties"].(map[string]interface{})
	if len(rangeFields) != 8 {
		t.Fatalf("time range fields are incomplete: %#v", rangeFields)
	}
	weekday := rangeFields["start_weekday"].(map[string]interface{})
	if len(weekday["enum"].([]string)) != 8 {
		t.Fatalf("weekday enum is incomplete: %#v", weekday)
	}
	month := rangeFields["start_month"].(map[string]interface{})
	if len(month["enum"].([]string)) != 13 {
		t.Fatalf("month enum is incomplete: %#v", month)
	}
	// A time condition must require both destinations and the time group.
	conditionSchema := map[string]interface{}{}
	for _, definition := range toolDefinitions() {
		if definition["name"] == "create_time_condition_plan" {
			conditionSchema = definition["inputSchema"].(map[string]interface{})
		}
	}
	conditionRequired := conditionSchema["required"].([]string)
	if len(conditionRequired) != 4 {
		t.Fatalf("create_time_condition_plan must require four fields: %#v", conditionRequired)
	}
}

func TestIvrToolsAreExposedAndGated(t *testing.T) {
	found := map[string]bool{}
	for _, definition := range toolDefinitions() {
		found[definition["name"].(string)] = true
	}
	for _, name := range []string{"list_ivrs", "create_ivr_plan", "update_ivr_plan", "delete_ivr_plan"} {
		if !found[name] {
			t.Fatalf("ivr tool %s is missing: %#v", name, found)
		}
	}
	if !toolAllowed("list_ivrs", map[string]bool{"assistant.ivr.read": true}) {
		t.Fatal("ivr read permission does not authorize list_ivrs")
	}
	planPermission := map[string]bool{"assistant.ivr.plan": true}
	for _, name := range []string{"create_ivr_plan", "update_ivr_plan", "delete_ivr_plan"} {
		if !toolAllowed(name, planPermission) {
			t.Fatalf("ivr plan permission does not authorize %s", name)
		}
		if !isPlanMutationTool(name) {
			t.Fatalf("%s is not audited as a plan mutation", name)
		}
	}
	if toolAllowed("create_ivr_plan", map[string]bool{"assistant.ivr.read": true}) {
		t.Fatal("ivr read permission authorized planning")
	}
	// The menu is bounded, each option is a single caller digit, and the update
	// tool must route its fields through changes.
	schema := map[string]interface{}{}
	for _, definition := range toolDefinitions() {
		if definition["name"] == "create_ivr_plan" {
			schema = definition["inputSchema"].(map[string]interface{})
		}
	}
	properties := schema["properties"].(map[string]interface{})
	entries := properties["entries"].(map[string]interface{})
	if entries["maxItems"] != 10 {
		t.Fatalf("ivr menu is not bounded: %#v", entries)
	}
	entrySchema := entries["items"].(map[string]interface{})
	if entrySchema["additionalProperties"] != false {
		t.Fatalf("a menu option accepts unknown fields: %#v", entrySchema)
	}
	digits := entrySchema["properties"].(map[string]interface{})["digits"].(map[string]interface{})
	if digits["pattern"] != "^[0-9]$" {
		t.Fatalf("menu digits are not a single keypress: %#v", digits)
	}
	required := schema["required"].([]string)
	if len(required) != 4 {
		t.Fatalf("create_ivr_plan must require four fields: %#v", required)
	}
	updateSchema := map[string]interface{}{}
	for _, definition := range toolDefinitions() {
		if definition["name"] == "update_ivr_plan" {
			updateSchema = definition["inputSchema"].(map[string]interface{})
		}
	}
	updateProperties := updateSchema["properties"].(map[string]interface{})
	changes := updateProperties["changes"].(map[string]interface{})
	if changes["additionalProperties"] != false || changes["minProperties"] != 1 {
		t.Fatalf("ivr changes are not constrained: %#v", changes)
	}
	if updateProperties["entries"] != nil {
		t.Fatal("entries must only be reachable through changes on update")
	}
}

func TestRedactionRemovesNestedSecrets(t *testing.T) {
	input := map[string]interface{}{"extension": "100", "secret": "sip", "nested": map[string]interface{}{"password": "pw", "safe": "ok"}}
	clean := redact(input).(map[string]interface{})
	if _, ok := clean["secret"]; ok {
		t.Fatal("secret was not redacted")
	}
	nested := clean["nested"].(map[string]interface{})
	if _, ok := nested["password"]; ok {
		t.Fatal("password was not redacted")
	}
	if nested["safe"] != "ok" {
		t.Fatal("safe value was removed")
	}
}

func TestPlanToolAuditKeepsSelectorsAndDropsSecrets(t *testing.T) {
	input := map[string]interface{}{
		"extensions":      []interface{}{"400", "401"},
		"start_extension": float64(400),
		"count":           float64(3),
		"end_extension":   "secret-in-wrong-field",
		"profile":         "pjsip_webrtc",
		"credentials":     map[string]interface{}{"password": "do-not-log"},
		"voicemail":       map[string]interface{}{"pin": "123456"},
		"name_pattern":    "Private customer name",
	}
	encoded := mustJSON(safePlanToolArguments(input))
	if !strings.Contains(encoded, `"selector_mode":"both"`) || !strings.Contains(encoded, `"count":3`) {
		t.Fatalf("selector audit is incomplete: %s", encoded)
	}
	for _, secret := range []string{"do-not-log", "123456", "Private customer name", "secret-in-wrong-field", "credentials", "voicemail"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("selector audit leaked %q: %s", secret, encoded)
		}
	}
}

func TestDiscriminatedRangeSelectorIgnoresListFields(t *testing.T) {
	args := map[string]interface{}{
		"selector": map[string]interface{}{
			"mode":            "range",
			"start_extension": float64(400),
			"count":           float64(2),
			"extensions":      []interface{}{"999"},
		},
		"extensions": []interface{}{"888"},
	}
	if err := normalizeExtensionSelector(args); err != nil {
		t.Fatal(err)
	}
	if args["start_extension"] != 400 || args["count"] != 2 {
		t.Fatalf("range selector was not normalized: %#v", args)
	}
	if _, exists := args["extensions"]; exists {
		t.Fatalf("list fields survived range normalization: %#v", args)
	}
	if _, exists := args["selector"]; exists {
		t.Fatalf("nested selector was sent to PBX API: %#v", args)
	}
}

func TestDiscriminatedListSelectorIgnoresRangeFields(t *testing.T) {
	args := map[string]interface{}{
		"selector": map[string]interface{}{
			"mode":            "list",
			"extensions":      []interface{}{"400", "401"},
			"start_extension": float64(900),
			"count":           float64(1),
		},
	}
	if err := normalizeExtensionSelector(args); err != nil {
		t.Fatal(err)
	}
	if _, exists := args["start_extension"]; exists {
		t.Fatalf("range fields survived list normalization: %#v", args)
	}
	if len(args["extensions"].([]interface{})) != 2 {
		t.Fatalf("list selector was not normalized: %#v", args)
	}
}

func TestEmptyLegacyListDoesNotConflictWithRange(t *testing.T) {
	args := map[string]interface{}{
		"extensions":      []interface{}{},
		"start_extension": float64(400),
		"count":           float64(2),
		"end_extension":   float64(401),
	}
	if err := normalizeExtensionSelector(args); err != nil {
		t.Fatal(err)
	}
	if _, exists := args["extensions"]; exists {
		t.Fatalf("empty legacy list was not removed: %#v", args)
	}
}

func TestHangupFailoverDropsProviderDestination(t *testing.T) {
	for _, destination := range []interface{}{"", "4030", nil} {
		original := map[string]interface{}{"type": "hangup", "destination_extension": destination}
		args := map[string]interface{}{"failover": original}
		if err := normalizeQueueArguments(args); err != nil {
			t.Fatal(err)
		}
		normalized := args["failover"].(map[string]interface{})
		if _, exists := normalized["destination_extension"]; exists {
			t.Fatalf("hangup destination survived normalization: %#v", normalized)
		}
		if _, exists := original["destination_extension"]; !exists {
			t.Fatal("normalization mutated the provider tool-call audit arguments")
		}
	}
}

func TestNonHangupFailoverRequiresDestination(t *testing.T) {
	args := map[string]interface{}{"failover": map[string]interface{}{"type": "extension", "destination_extension": ""}}
	if err := normalizeQueueArguments(args); err == nil {
		t.Fatal("empty extension failover destination was accepted")
	}
}

func TestProviderToolCallNormalization(t *testing.T) {
	tests := []struct {
		name         string
		provider     provider
		responseName string
	}{
		{"openai", &openAIProvider{client: jsonClient(`{"id":"resp_one","output":[{"type":"function_call","call_id":"one","name":"list_extensions","arguments":"{}"}]}`)}, "list_extensions"},
		{"anthropic", &anthropicProvider{client: jsonClient(`{"content":[{"type":"tool_use","id":"two","name":"get_extension","input":{"extension":"100"}}]}`)}, "get_extension"},
		{"gemini", &geminiProvider{client: jsonClient(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"create_extension_plan","args":{"profile":"pjsip"}}}]}}]}`)}, "create_extension_plan"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reply, err := tt.provider.Reply(context.Background(), "key", "model", []chatMessage{{Role: "user", Content: "test"}}, toolDefinitions(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(reply.Calls) != 1 || reply.Calls[0].Name != tt.responseName {
				t.Fatalf("unexpected normalized reply: %#v", reply)
			}
		})
	}
}

func TestOpenAIUsesResponsesToolContract(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected OpenAI endpoint: %s", req.URL.Path)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, legacy := body["messages"]; legacy {
			t.Fatal("legacy Chat Completions messages were sent")
		}
		tools := body["tools"].([]interface{})
		tool := tools[0].(map[string]interface{})
		if tool["name"] == nil || tool["parameters"] == nil || tool["function"] != nil {
			t.Fatalf("unexpected Responses tool shape: %#v", tool)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"resp_1","output":[{"type":"function_call","call_id":"call_1","name":"list_extensions","arguments":"{}"}]}`)), Request: req}, nil
	})}
	p := &openAIProvider{client: client}
	reply, err := p.Reply(context.Background(), "key", "gpt-test", []chatMessage{{Role: "user", Content: "list"}}, toolDefinitions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if reply.ResponseID != "resp_1" || len(reply.Calls) != 1 {
		t.Fatalf("unexpected first response: %#v", reply)
	}
}

func TestOpenAIContinuesWithFunctionCallOutput(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body map[string]interface{}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["previous_response_id"] != "resp_1" {
			t.Fatalf("missing previous response: %#v", body)
		}
		input := body["input"].([]interface{})
		item := input[0].(map[string]interface{})
		if item["type"] != "function_call_output" || item["call_id"] != "call_1" {
			t.Fatalf("unexpected function output: %#v", item)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"resp_2","output":[{"type":"message","content":[{"type":"output_text","text":"Done"}]}]}`)), Request: req}, nil
	})}
	p := &openAIProvider{client: client}
	reply, err := p.Reply(context.Background(), "key", "gpt-test", nil, toolDefinitions(), []toolResult{{ID: "call_1", Name: "list_extensions", Value: map[string]interface{}{"results": []interface{}{}}, ProviderResponseID: "resp_1"}})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Text != "Done" {
		t.Fatalf("unexpected response text: %q", reply.Text)
	}
}

func TestNaturalLanguageRangeAmbiguity(t *testing.T) {
	if !ambiguousNaturalRange("crea 10 extensiones WebRTC desde la 100 a la 110") {
		t.Fatal("expected contradictory range to be ambiguous")
	}
	if ambiguousNaturalRange("create 10 extensions from 100 to 109") {
		t.Fatal("matching inclusive range should not be ambiguous")
	}
	if !ambiguousNaturalRange("crear 10 extensiones entre 100 y 110") {
		t.Fatal("expected between range to be ambiguous")
	}
}

func TestPBXInsecureTLSIsRestrictedToLoopback(t *testing.T) {
	local, _ := url.Parse("https://127.0.0.1/pbxapi")
	transport, err := newPBXTransport(config{PBXAPITLSInsecure: true}, local)
	if err != nil {
		t.Fatal(err)
	}
	if transport.TLSClientConfig == nil || !transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("loopback TLS exception was not applied")
	}

	remote, _ := url.Parse("https://pbx.example.com/pbxapi")
	if _, err := newPBXTransport(config{PBXAPITLSInsecure: true}, remote); err == nil {
		t.Fatal("remote TLS verification must never be disabled")
	}
}

func TestPBXAPIErrorIncludesSafeCorrelationID(t *testing.T) {
	err := pbxAPIError(422, map[string]interface{}{
		"status":     "ambiguous",
		"detail":     "Use either extensions or start_extension/count, not both",
		"request_id": "abc123",
	})
	if !strings.Contains(err.Error(), "request_id=abc123") || !strings.Contains(err.Error(), "Use either extensions") {
		t.Fatalf("unexpected correlated error: %v", err)
	}
}

func TestEquivalentExtensionSelectorsAreCanonicalized(t *testing.T) {
	args := map[string]interface{}{
		"extensions":      []interface{}{"400", "401"},
		"start_extension": float64(400),
		"count":           float64(2),
		"end_extension":   float64(401),
	}
	canonicalizeEquivalentExtensionSelector(args)
	for _, key := range []string{"start_extension", "count", "end_extension"} {
		if _, exists := args[key]; exists {
			t.Fatalf("redundant selector %s was not removed: %#v", key, args)
		}
	}
}

func TestConflictingExtensionSelectorsRemainForAPIAudit(t *testing.T) {
	args := map[string]interface{}{
		"extensions":      []interface{}{"400", "401"},
		"start_extension": float64(400),
		"count":           float64(3),
	}
	canonicalizeEquivalentExtensionSelector(args)
	if _, exists := args["start_extension"]; !exists {
		t.Fatalf("conflicting selector was silently accepted: %#v", args)
	}
}
