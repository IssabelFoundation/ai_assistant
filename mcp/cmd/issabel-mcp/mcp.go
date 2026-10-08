package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type toolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

func runStdio(ctx context.Context, input io.Reader, output io.Writer, pbx *pbxClient, user string) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		var request rpcRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			_ = encoder.Encode(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		if len(request.ID) == 0 {
			continue
		}
		var id interface{}
		_ = json.Unmarshal(request.ID, &id)
		response := rpcResponse{JSONRPC: "2.0", ID: id}
		switch request.Method {
		case "initialize":
			response.Result = map[string]interface{}{
				"protocolVersion": "2025-03-26",
				"capabilities":    map[string]interface{}{"tools": map[string]interface{}{"listChanged": false}},
				"serverInfo":      map[string]interface{}{"name": "issabel-mcp", "version": "0.1.0"},
				"instructions":    "PBX changes are staged as plans. A human must approve and execute them in Issabel. Verify every number with check_number before proposing it: an extension lookup that returns nothing does not mean the number is free.",
			}
		case "ping":
			response.Result = map[string]interface{}{}
		case "tools/list":
			response.Result = map[string]interface{}{"tools": toolDefinitions()}
		case "tools/call":
			var params toolCallParams
			if err := json.Unmarshal(request.Params, &params); err != nil {
				response.Error = &rpcError{Code: -32602, Message: "invalid tool arguments"}
				break
			}
			result, err := pbx.tool(ctx, user, params.Name, params.Arguments)
			if err != nil {
				response.Result = map[string]interface{}{"isError": true, "content": []map[string]string{{"type": "text", "text": err.Error()}}}
			} else {
				encoded, _ := json.Marshal(result)
				response.Result = map[string]interface{}{"content": []map[string]string{{"type": "text", "text": string(encoded)}}, "structuredContent": result}
			}
		default:
			response.Error = &rpcError{Code: -32601, Message: "method not found"}
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func toolDefinitions() []map[string]interface{} {
	extensionSelector := map[string]interface{}{
		"type":                 "object",
		"description":          "Required extension selection. Set mode to list and provide only extensions, or set mode to range and provide only start_extension and count.",
		"required":             []string{"mode"},
		"additionalProperties": false,
		"properties": map[string]interface{}{
			"mode":            map[string]interface{}{"type": "string", "enum": []string{"list", "range"}, "description": "Authoritative selector mode. Fields from the other mode are ignored."},
			"extensions":      map[string]interface{}{"type": "array", "description": "Required only when mode is list. Explicit non-empty extension list.", "items": map[string]interface{}{"type": "string", "pattern": "^[0-9]{1,8}$"}, "minItems": 1, "maxItems": 100},
			"start_extension": map[string]interface{}{"type": "integer", "description": "Required only when mode is range. First extension.", "minimum": 0, "maximum": 99999999},
			"count":           map[string]interface{}{"type": "integer", "description": "Required only when mode is range. Number of extensions beginning at start_extension.", "minimum": 1, "maximum": 100},
		},
	}
	selectorProperty := map[string]interface{}{"selector": extensionSelector}
	createProps := cloneSchema(selectorProperty)
	createProps["profile"] = map[string]interface{}{"type": "string", "enum": []string{"sip", "pjsip", "pjsip_webrtc"}, "description": "Extension profile. Default codecs: pjsip_webrtc uses opus only; sip and pjsip use ulaw and alaw."}
	createProps["name_pattern"] = map[string]string{"type": "string"}
	createProps["voicemail"] = map[string]interface{}{"type": "object", "required": []string{"enabled"}, "properties": map[string]interface{}{"enabled": map[string]string{"type": "boolean"}, "pin_mode": map[string]interface{}{"type": "string", "enum": []string{"generate", "provided_at_execution"}}}}
	createProps["credentials"] = map[string]interface{}{"type": "object", "properties": map[string]interface{}{"password_mode": map[string]interface{}{"type": "string", "enum": []string{"generate", "provided_at_execution"}}}}
	createProps["codecs"] = map[string]interface{}{"type": "array", "description": "When creating with default codecs, send [\"opus\"] for pjsip_webrtc and [\"ulaw\",\"alaw\"] for sip or pjsip. On update, omit codecs unless a codec change was requested; omission preserves existing codecs.", "items": map[string]interface{}{"type": "string", "enum": []string{"ulaw", "alaw", "opus", "g722", "gsm"}}}
	createProps["context"] = map[string]interface{}{"type": "string", "enum": []string{"from-internal"}}
	updateChanges := map[string]interface{}{"type": "object", "additionalProperties": false, "properties": map[string]interface{}{
		"name":        map[string]string{"type": "string"},
		"context":     map[string]interface{}{"type": "string", "enum": []string{"from-internal"}},
		"codecs":      createProps["codecs"],
		"voicemail":   map[string]interface{}{"type": "object", "required": []string{"enabled"}, "properties": map[string]interface{}{"enabled": map[string]string{"type": "boolean"}, "pin_mode": map[string]interface{}{"type": "string", "enum": []string{"generate", "provided_at_execution"}}}, "additionalProperties": false},
		"credentials": map[string]interface{}{"type": "object", "required": []string{"password_mode"}, "properties": map[string]interface{}{"password_mode": map[string]interface{}{"type": "string", "enum": []string{"generate", "provided_at_execution"}}}, "additionalProperties": false},
	}}
	agentItem := map[string]interface{}{"type": "object", "additionalProperties": false, "required": []string{"extension", "penalty"}, "properties": map[string]interface{}{
		"extension": map[string]interface{}{"type": "string", "pattern": "^[0-9]{1,8}$"},
		"penalty":   map[string]interface{}{"type": "integer", "minimum": 0, "maximum": 100},
	}}
	// Destination families a plan may target; keep in sync with
	// mcpplans::destinationFamilies().
	destinationTypeEnum := []string{"hangup", "extension", "queue", "ring_group", "ivr"}
	// Shared by queues and ring groups: the same allowlisted destination model.
	failoverProperty := map[string]interface{}{"type": "object", "additionalProperties": false, "required": []string{"type"}, "properties": map[string]interface{}{
		"type":                  map[string]interface{}{"type": "string", "enum": destinationTypeEnum, "description": "Authoritative failover mode."},
		"destination_extension": map[string]interface{}{"type": "string", "pattern": "^[0-9]{1,8}$", "description": "Required for extension, queue, and ring_group. Omit this field entirely for hangup."},
	}}
	queueProps := map[string]interface{}{
		"extension": map[string]interface{}{"type": "string", "pattern": "^[0-9]{1,8}$", "description": "Queue extension."},
		"name":      map[string]interface{}{"type": "string", "minLength": 1, "maxLength": 80},
		"strategy":  map[string]interface{}{"type": "string", "enum": []string{"ringall", "leastrecent", "fewestcalls", "random", "rrmemory", "rrordered", "linear", "wrandom"}},
		"agents": map[string]interface{}{"type": "object", "additionalProperties": false, "required": []string{"static", "dynamic"}, "properties": map[string]interface{}{
			"static":  map[string]interface{}{"type": "array", "description": "Static queue agents. Use an empty array when none are requested.", "items": agentItem, "maxItems": 100},
			"dynamic": map[string]interface{}{"type": "array", "description": "Dynamic queue agents. Use an empty array when none are requested.", "items": agentItem, "maxItems": 100},
		}},
		"max_wait_seconds":      map[string]interface{}{"type": "integer", "minimum": 0, "maximum": 86400, "description": "Maximum caller wait before failover. Zero means unlimited. A timeout mentioned together with failover normally refers to this field."},
		"agent_timeout_seconds": map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 3600, "description": "Seconds to ring each agent per attempt. Omit to use the visible plan default of 15 seconds."},
		"retry_seconds":         map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 300, "description": "Seconds before retrying agents. Omit to use the visible plan default of 5 seconds."},
		"wrapup_seconds":        map[string]interface{}{"type": "integer", "minimum": 0, "maximum": 3600, "description": "Agent wrap-up time. Omit to use the visible plan default of zero."},
		"failover":              failoverProperty,
	}
	timeIdProperty := map[string]interface{}{"type": "string", "pattern": "^[0-9]{1,8}$"}
	ivrEntry := map[string]interface{}{"type": "object", "additionalProperties": false, "required": []string{"digits", "destination"}, "properties": map[string]interface{}{
		"digits":        map[string]interface{}{"type": "string", "pattern": "^[0-9]$", "description": "Key the caller presses, 0 to 9."},
		"destination":   failoverProperty,
		"return_to_ivr": map[string]interface{}{"type": "boolean", "description": "Return to the parent IVR after the destination finishes. Defaults to true."},
	}}
	ivrProps := map[string]interface{}{
		"name":                map[string]interface{}{"type": "string", "minLength": 1, "maxLength": 80},
		"description":         map[string]interface{}{"type": "string", "maxLength": 150},
		"announcement":        map[string]interface{}{"type": "integer", "minimum": 0, "description": "Recording id used as the greeting."},
		"timeout_seconds":     map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 300, "description": "Seconds to wait for a key. Defaults to 10."},
		"timeout_destination": failoverProperty,
		"invalid_destination": failoverProperty,
		"entries":             map[string]interface{}{"type": "array", "maxItems": 10, "items": ivrEntry, "description": "Menu options. Send an empty array for a menu with no options."},
	}
	ivrUpdateProps := map[string]interface{}{"type": "object", "additionalProperties": false, "minProperties": 1, "properties": map[string]interface{}{
		"name":                ivrProps["name"],
		"description":         ivrProps["description"],
		"announcement":        ivrProps["announcement"],
		"timeout_seconds":     ivrProps["timeout_seconds"],
		"timeout_destination": failoverProperty,
		"invalid_destination": failoverProperty,
		"entries":             ivrProps["entries"],
	}}
	ivrIdProperty := map[string]interface{}{"type": "string", "pattern": "^[0-9]{1,8}$"}
	weekdayEnum := []string{"*", "mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	monthEnum := []string{"*", "jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
	timeRange := map[string]interface{}{"type": "object", "additionalProperties": false, "properties": map[string]interface{}{
		"start_hour":     map[string]interface{}{"type": "string", "pattern": "^([01][0-9]|2[0-3]):[0-5][0-9]$", "description": "HH:MM. Defaults to 00:00."},
		"end_hour":       map[string]interface{}{"type": "string", "pattern": "^([01][0-9]|2[0-3]):[0-5][0-9]$", "description": "HH:MM. Defaults to 23:59."},
		"start_weekday":  map[string]interface{}{"type": "string", "enum": weekdayEnum, "description": "Defaults to *."},
		"end_weekday":    map[string]interface{}{"type": "string", "enum": weekdayEnum, "description": "Defaults to the start weekday."},
		"start_monthday": map[string]interface{}{"type": "string", "description": "1-31 or *. Defaults to *."},
		"end_monthday":   map[string]interface{}{"type": "string", "description": "1-31 or *. Defaults to the start monthday."},
		"start_month":    map[string]interface{}{"type": "string", "enum": monthEnum, "description": "Defaults to *."},
		"end_month":      map[string]interface{}{"type": "string", "enum": monthEnum, "description": "Defaults to the start month."},
	}}
	timeGroupProps := map[string]interface{}{
		"name":  map[string]interface{}{"type": "string", "minLength": 1, "maxLength": 80},
		"times": map[string]interface{}{"type": "array", "minItems": 1, "maxItems": 50, "items": timeRange, "description": "Ranges that make up the group."},
	}
	timeConditionProps := map[string]interface{}{
		"name":           map[string]interface{}{"type": "string", "minLength": 1, "maxLength": 80},
		"time_group_id":  map[string]interface{}{"type": "string", "pattern": "^[0-9]{1,8}$", "description": "Id of an existing time group. Use list_timegroups to find one."},
		"matches":        failoverProperty,
		"does_not_match": failoverProperty,
	}
	ringGroupProps := map[string]interface{}{
		"extension":         map[string]interface{}{"type": "string", "pattern": "^[0-9]{1,8}$", "description": "Ring group number."},
		"name":              map[string]interface{}{"type": "string", "minLength": 1, "maxLength": 80},
		"strategy":          map[string]interface{}{"type": "string", "enum": []string{"ringall", "ringall-prim", "hunt", "hunt-prim", "memoryhunt", "memoryhunt-prim", "firstavailable", "firstnotonphone"}},
		"members":           map[string]interface{}{"type": "array", "minItems": 1, "maxItems": 100, "items": map[string]interface{}{"type": "string", "pattern": "^[0-9]{1,8}$"}, "description": "Existing extensions that ring."},
		"ring_time_seconds": map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 300, "description": "Seconds to ring before failover. Omit to use the visible plan default of 20 seconds."},
		"failover":          failoverProperty,
	}

	return []map[string]interface{}{
		tool("list_extensions", "List extensions without returning credentials. Only real extensions are listed, so a number missing here may still be reserved by another PBX module. Use check_number before proposing a number.", objectSchema(map[string]interface{}{}, nil)),
		tool("get_extension", "Get one extension without returning credentials. A not-found result only means no extension has that number; it does NOT mean the number is free. Use check_number for availability.", objectSchema(map[string]interface{}{"extension": map[string]string{"type": "string"}}, []string{"extension"})),
		tool("list_queues", "List queues and their non-secret effective configuration, including static and dynamic agents.", objectSchema(map[string]interface{}{}, nil)),
		tool("check_number", "Report whether one number can be used. Returns available=false and the owning module when a queue, ring group, conference, parking lot, custom extension or feature code already reserves it, or when it is already an extension. Call this for every number before creating an extension plan.", objectSchema(map[string]interface{}{"extension": map[string]interface{}{"type": "string", "pattern": "^[0-9]{1,8}$", "description": "Number to check."}}, []string{"extension"})),
		tool("check_numbers", "Check several numbers in one call and get back which are free and which are unavailable. Use this whenever a request mentions more than one number, including answering which numbers are available. Every number you report must come from the free list of a call like this one.", objectSchema(map[string]interface{}{"numbers": map[string]interface{}{"type": "array", "minItems": 1, "maxItems": 100, "items": map[string]interface{}{"type": "string", "pattern": "^[0-9]{1,8}$"}, "description": "Numbers to check."}}, []string{"numbers"})),
		tool("list_numbers", "List every number already in use across the PBX with the owning module (extension, queue, ring group, conference, parking lot, custom extension or feature code). Call this before proposing a new number, so a plan is never created for a number the PBX reserves. For a specific number or a short range use check_number or check_numbers instead.", objectSchema(map[string]interface{}{}, nil)),
		tool("list_destinations", "List the dialplan destinations that exist on this PBX, with their type and label. Use it to pick a real failover or routing target instead of inventing one.", objectSchema(map[string]interface{}{}, nil)),
		tool("create_extension_plan", "Create an immutable, non-secret extension plan pending human approval. Selector and voicemail must be explicit.", objectSchema(createProps, []string{"selector", "profile", "voicemail"})),
		tool("update_extension_plan", "Create a pending plan to update extensions or rotate device passwords and voicemail PINs without storing secrets.", objectSchema(mergeSchema(selectorProperty, map[string]interface{}{"changes": updateChanges}), []string{"selector", "changes"})),
		tool("delete_extension_plan", "Create a pending plan to delete existing extensions.", objectSchema(selectorProperty, []string{"selector"})),
		tool("create_queue_plan", "Create a non-secret queue plan pending human approval. Static and dynamic agents must both be explicit arrays. Timeout before failover is max_wait_seconds.", objectSchema(queueProps, []string{"extension", "name", "strategy", "agents", "max_wait_seconds", "failover"})),
		tool("delete_queue_plan", "Create a pending plan to delete one existing queue. This does not execute the deletion.", objectSchema(map[string]interface{}{"extension": map[string]interface{}{"type": "string", "pattern": "^[0-9]{1,8}$"}}, []string{"extension"})),
		tool("list_ringgroups", "List ring groups and their non-secret configuration: strategy, ring time, members and failover.", objectSchema(map[string]interface{}{}, nil)),
		tool("create_ring_group_plan", "Create a non-secret ring group plan pending human approval. Members must be existing extensions and the failover uses the same allowlist as queues.", objectSchema(ringGroupProps, []string{"extension", "name", "members", "strategy", "failover"})),
		tool("update_ring_group_plan", "Create a pending plan to update one ring group: rename it, or change its members, strategy, ring time or failover. Send only the fields that change; every other field is rejected.", objectSchema(mergeSchema(map[string]interface{}{"extension": ringGroupProps["extension"]}, map[string]interface{}{"changes": map[string]interface{}{"type": "object", "additionalProperties": false, "minProperties": 1, "properties": map[string]interface{}{
			"name":              map[string]interface{}{"type": "string", "minLength": 1, "maxLength": 80},
			"members":           ringGroupProps["members"],
			"strategy":          ringGroupProps["strategy"],
			"ring_time_seconds": ringGroupProps["ring_time_seconds"],
			"failover":          failoverProperty,
		}}}), []string{"extension", "changes"})),
		tool("delete_ring_group_plan", "Create a pending plan to delete one existing ring group. This does not execute the deletion.", objectSchema(map[string]interface{}{"extension": map[string]interface{}{"type": "string", "pattern": "^[0-9]{1,8}$"}}, []string{"extension"})),
		tool("list_timegroups", "List time groups with their ranges: hours, weekdays, monthdays and months.", objectSchema(map[string]interface{}{}, nil)),
		tool("list_timeconditions", "List time conditions with the time group they use and both destinations decoded.", objectSchema(map[string]interface{}{}, nil)),
		tool("create_time_group_plan", "Create a non-secret time group plan pending human approval. Every range is validated and an omitted field keeps its documented default.", objectSchema(timeGroupProps, []string{"name", "times"})),
		tool("delete_time_group_plan", "Create a pending plan to delete one time group. It is refused while a time condition still uses that group.", objectSchema(map[string]interface{}{"id": timeIdProperty}, []string{"id"})),
		tool("create_time_condition_plan", "Create a non-secret time condition plan pending human approval. Both destinations use the same allowlist as queues and ring groups.", objectSchema(timeConditionProps, []string{"name", "time_group_id", "matches", "does_not_match"})),
		tool("delete_time_condition_plan", "Create a pending plan to delete one time condition. This does not execute the deletion.", objectSchema(map[string]interface{}{"id": timeIdProperty}, []string{"id"})),
		tool("list_ivrs", "List IVRs with their greeting, timeout, both fallback destinations and every menu option decoded.", objectSchema(map[string]interface{}{}, nil)),
		tool("create_ivr_plan", "Create a non-secret IVR plan pending human approval. Both fallback destinations and every menu option must be explicit; entries may be an empty array.", objectSchema(ivrProps, []string{"name", "timeout_destination", "invalid_destination", "entries"})),
		tool("update_ivr_plan", "Create a pending plan to update one IVR. Send only the fields that change; when entries is sent it replaces the whole menu.", objectSchema(mergeSchema(map[string]interface{}{"id": ivrIdProperty}, map[string]interface{}{"changes": ivrUpdateProps}), []string{"id", "changes"})),
		tool("delete_ivr_plan", "Create a pending plan to delete one IVR and its menu. This does not execute the deletion.", objectSchema(map[string]interface{}{"id": ivrIdProperty}, []string{"id"})),
		tool("get_plan_status", "Read a plan status and non-secret summary.", objectSchema(map[string]interface{}{"plan_id": map[string]string{"type": "string"}}, []string{"plan_id"})),
		tool("cancel_plan", "Cancel a pending or approved plan; this never executes it.", objectSchema(map[string]interface{}{"plan_id": map[string]string{"type": "string"}}, []string{"plan_id"})),
	}
}

func tool(name, description string, schema map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"name": name, "description": description, "inputSchema": schema}
}

func objectSchema(properties map[string]interface{}, required []string) map[string]interface{} {
	schema := map[string]interface{}{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func cloneSchema(source map[string]interface{}) map[string]interface{} {
	return mergeSchema(source, nil)
}
func mergeSchema(first, second map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(first)+len(second))
	for k, v := range first {
		result[k] = v
	}
	for k, v := range second {
		result[k] = v
	}
	return result
}
