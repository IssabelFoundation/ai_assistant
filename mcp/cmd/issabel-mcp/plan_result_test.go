package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPlanID = "b6dde184-51d3-4d3f-a92a-a14c0204eac2"

func resultStore(t *testing.T) *store {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte(strings.Repeat("k", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := newStore(config{MasterKey: key, StateDir: filepath.Join(dir, "state")})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestPlanResultPersistence(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		s := resultStore(t)
		c := conversation{ID: strings.Repeat("a", 32), Title: "Colas", CreatedAt: 1}
		if legacy {
			c.Messages = []chatMessage{{Role: "assistant", Content: "Pendiente: " + testPlanID}}
		} else {
			c.PlanIDs = []string{testPlanID}
		}
		if err := s.saveConversation("alice", c); err != nil {
			t.Fatal(err)
		}
		input := planResultInput{PlanID: testPlanID, Outcome: "executed"}
		for i := 0; i < 2; i++ {
			if err := s.recordPlanResult("alice", input, planResultMessage(input.PlanID, input.Outcome)); err != nil {
				t.Fatal(err)
			}
		}
		// A stale model response must not overwrite the event that arrived meanwhile.
		c.Messages = append(c.Messages, chatMessage{Role: "assistant", Content: "Otra respuesta"})
		if err := s.saveConversation("alice", c); err != nil {
			t.Fatal(err)
		}
		loaded, err := s.loadConversation("alice", c.ID)
		if err != nil {
			t.Fatal(err)
		}
		events := 0
		for _, m := range loaded.Messages {
			if m.Kind == "plan_result" {
				events++
				if !strings.Contains(m.Content, "éxito") {
					t.Fatal(m)
				}
			}
		}
		if events != 1 {
			t.Fatalf("expected one persisted event, got %d", events)
		}
		if _, err = s.loadConversation("bob", c.ID); err == nil {
			t.Fatal("cross-user history leak")
		}
	}
}
func TestPlanResultRoute(t *testing.T) {
	s := resultStore(t)
	a := &apiServer{store: s}
	for _, tc := range []struct {
		method, body string
		allowed      bool
		status       int
	}{
		{"POST", `{"plan_id":"` + testPlanID + `","outcome":"executed"}`, true, 200},
		{"POST", `{"plan_id":"` + testPlanID + `","outcome":"execution_failed"}`, true, 200},
		{"POST", `{"plan_id":"` + testPlanID + `","outcome":"unknown"}`, true, 200},
		{"POST", `{"plan_id":"` + testPlanID + `","outcome":"approval_failed"}`, true, 200},
		{"POST", `{"plan_id":"` + testPlanID + `","outcome":"invented"}`, true, 422},
		{"POST", `{"plan_id":"../escape","outcome":"executed"}`, true, 422},
		{"POST", `{}`, false, 403}, {"GET", `{}`, true, 405},
	} {
		r := httptest.NewRequest(tc.method, "/v1/plan-result", strings.NewReader(tc.body))
		r = r.WithContext(context.WithValue(r.Context(), authUserKey{}, principal{User: "alice", Permissions: map[string]bool{"assistant.plans.approve": tc.allowed}}))
		w := httptest.NewRecorder()
		a.route(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: got %d: %s", tc.body, w.Code, w.Body.String())
		}
	}
	items, err := s.listConversations("alice")
	if err != nil || len(items) != 1 {
		t.Fatalf("fallback history: %v %v", items, err)
	}
	c, err := s.loadConversation("alice", items[0].ID)
	if err != nil || len(c.Messages) != 4 {
		t.Fatalf("outcomes not persisted: %v %v", c, err)
	}
	// The endpoint is behind the signed proxy authentication middleware.
	w := httptest.NewRecorder()
	a.auth(http.HandlerFunc(a.route)).ServeHTTP(w, httptest.NewRequest("POST", "/v1/plan-result", strings.NewReader(`{}`)))
	if w.Code != 401 {
		t.Fatalf("unsigned request accepted: %d", w.Code)
	}
}
