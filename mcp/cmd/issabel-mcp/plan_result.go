package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type planResultInput struct {
	PlanID  string `json:"plan_id"`
	Outcome string `json:"outcome"`
}

func planResultMessage(id, outcome string) string {
	switch outcome {
	case "executed":
		return "El plan " + id + " ha sido ejecutado con éxito."
	case "approval_failed":
		return "No se pudo aprobar el plan " + id + ". No se solicitó su ejecución."
	case "execution_failed":
		return "La ejecución del plan " + id + " devolvió un error. Revisá su auditoría antes de volver a intentarlo."
	default:
		return "No se pudo confirmar el resultado del plan " + id + ". Revisá su estado y auditoría antes de volver a intentarlo."
	}
}

// Only the authenticated PHP proxy reports outcomes, never an LLM tool or a
// browser-provided message. No execution bodies or credential CSVs are stored.
func (a *apiServer) planResult(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, "assistant.plans.approve") {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed")
		return
	}
	var input planResultInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if !regexp.MustCompile(`^[a-f0-9-]{36}$`).MatchString(input.PlanID) {
		writeError(w, 422, "invalid plan id")
		return
	}
	switch input.Outcome {
	case "executed", "approval_failed", "execution_failed", "unknown":
	default:
		writeError(w, 422, "invalid outcome")
		return
	}
	message := planResultMessage(input.PlanID, input.Outcome)
	if err := a.store.recordPlanResult(requestUser(r), input, message); err != nil {
		writeError(w, 500, "plan result could not be saved")
		return
	}
	writeJSON(w, 200, map[string]string{"message": message})
}

func (s *store) recordPlanResult(user string, result planResultInput, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.conversationDir(user)
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var target *conversation
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return err
		}
		var c conversation
		if err = json.Unmarshal(raw, &c); err != nil {
			return err
		}
		matches := false
		for _, id := range c.PlanIDs {
			if id == result.PlanID {
				matches = true
			}
		}
		// Compatibility with conversations created before plan IDs were stored.
		if !matches {
			for _, m := range c.Messages {
				if m.Role == "assistant" && strings.Contains(m.Content, result.PlanID) {
					matches = true
				}
			}
		}
		if matches && (target == nil || c.CreatedAt < target.CreatedAt) {
			copy := c
			target = &copy
		}
	}
	now := time.Now().Unix()
	if target == nil {
		target = &conversation{ID: randomHex(16), Title: "Resultado del plan " + result.PlanID, PlanIDs: []string{result.PlanID}, CreatedAt: now}
	}
	eventID := result.PlanID + ":" + result.Outcome
	for _, m := range target.Messages {
		if m.EventID == eventID {
			return nil
		}
	}
	target.Messages = append(target.Messages, chatMessage{Role: "assistant", Kind: "plan_result", EventID: eventID, Content: text, CreatedAt: now})
	target.UpdatedAt = now
	if !validID(target.ID) {
		return fmt.Errorf("invalid conversation id")
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(target)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, target.ID+".json"), data, 0600)
}
