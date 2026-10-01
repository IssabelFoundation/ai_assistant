package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type providerConfig struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	APIKey    string `json:"api_key"`
	UpdatedAt int64  `json:"updated_at"`
}

type providerView struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	HasKey    bool   `json:"has_key"`
	KeySuffix string `json:"key_suffix,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
}

type chatMessage struct {
	EventID   string `json:"event_id,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"created_at"`
}

type conversation struct {
	PlanIDs   []string      `json:"plan_ids,omitempty"`
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	Messages  []chatMessage `json:"messages"`
	CreatedAt int64         `json:"created_at"`
	UpdatedAt int64         `json:"updated_at"`
}

type store struct {
	cfg  config
	aead cipher.AEAD
	mu   sync.Mutex
}

func newStore(cfg config) (*store, error) {
	key, err := readSecret(cfg.MasterKey, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	for _, sub := range []string{"providers", "conversations"} {
		if err := os.MkdirAll(filepath.Join(cfg.StateDir, sub), 0700); err != nil {
			return nil, err
		}
	}
	return &store{cfg: cfg, aead: aead}, nil
}

func (s *store) Close() error { return nil }

func readSecret(path string, size int) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	raw = []byte(strings.TrimSpace(string(raw)))
	if decoded, decodeErr := base64.StdEncoding.DecodeString(string(raw)); decodeErr == nil && len(decoded) == size {
		return decoded, nil
	}
	if len(raw) == size {
		return raw, nil
	}
	return nil, fmt.Errorf("%s must contain %d raw bytes or their Base64 encoding", path, size)
}

func userID(user string) string {
	h := sha256.Sum256([]byte(user))
	return hex.EncodeToString(h[:])
}

func (s *store) providerPath(user string) string {
	return filepath.Join(s.cfg.StateDir, "providers", userID(user)+".enc")
}

func (s *store) conversationDir(user string) string {
	return filepath.Join(s.cfg.StateDir, "conversations", userID(user))
}

func (s *store) encrypt(plain []byte, aad string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return append(nonce, s.aead.Seal(nil, nonce, plain, []byte(aad))...), nil
}

func (s *store) decrypt(data []byte, aad string) ([]byte, error) {
	if len(data) < s.aead.NonceSize() {
		return nil, errors.New("encrypted value is truncated")
	}
	nonce, ciphertext := data[:s.aead.NonceSize()], data[s.aead.NonceSize():]
	return s.aead.Open(nil, nonce, ciphertext, []byte(aad))
}

func (s *store) saveProvider(user string, pc providerConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pc.UpdatedAt = time.Now().Unix()
	plain, err := json.Marshal(pc)
	if err != nil {
		return err
	}
	data, err := s.encrypt(plain, "provider:"+user)
	if err != nil {
		return err
	}
	return atomicWrite(s.providerPath(user), data, 0600)
}

func (s *store) loadProvider(user string) (providerConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var pc providerConfig
	data, err := os.ReadFile(s.providerPath(user))
	if err != nil {
		return pc, err
	}
	plain, err := s.decrypt(data, "provider:"+user)
	if err != nil {
		return pc, err
	}
	err = json.Unmarshal(plain, &pc)
	return pc, err
}

func (s *store) providerView(user string) (providerView, error) {
	pc, err := s.loadProvider(user)
	if err != nil {
		return providerView{}, err
	}
	suffix := pc.APIKey
	if len(suffix) > 4 {
		suffix = suffix[len(suffix)-4:]
	}
	return providerView{Provider: pc.Provider, Model: pc.Model, HasKey: pc.APIKey != "", KeySuffix: suffix, UpdatedAt: pc.UpdatedAt}, nil
}

func (s *store) deleteProvider(user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.providerPath(user))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *store) saveConversation(user string, c conversation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validID(c.ID) {
		return errors.New("invalid conversation id")
	}
	dir := s.conversationDir(user)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// A chat turn may have loaded this conversation before an execution finished.
	// Preserve server events written while the model was responding.
	if raw, err := os.ReadFile(filepath.Join(dir, c.ID+".json")); err == nil {
		var current conversation
		if err := json.Unmarshal(raw, &current); err != nil {
			return err
		}
		for _, message := range current.Messages {
			if message.EventID == "" {
				continue
			}
			found := false
			for _, existing := range c.Messages {
				if existing.EventID == message.EventID {
					found = true
					break
				}
			}
			if !found {
				c.Messages = append(c.Messages, message)
			}
		}
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, c.ID+".json"), data, 0600)
}

func (s *store) loadConversation(user, id string) (conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var c conversation
	if !validID(id) {
		return c, errors.New("invalid conversation id")
	}
	data, err := os.ReadFile(filepath.Join(s.conversationDir(user), id+".json"))
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(data, &c)
	return c, err
}

func (s *store) listConversations(user string) ([]conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.conversationDir(user))
	if os.IsNotExist(err) {
		return []conversation{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]conversation, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(s.conversationDir(user), entry.Name()))
		if readErr != nil {
			continue
		}
		var c conversation
		if json.Unmarshal(data, &c) == nil {
			c.Messages = nil
			result = append(result, c)
		}
	}
	return result, nil
}

func (s *store) deleteConversation(user, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validID(id) {
		return errors.New("invalid conversation id")
	}
	err := os.Remove(filepath.Join(s.conversationDir(user), id+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *store) cleanup() error {
	cutoff := time.Now().Add(-s.cfg.HistoryTTL)
	root := filepath.Join(s.cfg.StateDir, "conversations")
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && info.ModTime().Before(cutoff) {
			return os.Remove(path)
		}
		return nil
	})
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".issabel-mcp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func validID(id string) bool {
	if len(id) < 16 || len(id) > 64 {
		return false
	}
	for _, ch := range id {
		if (ch < 'a' || ch > 'f') && (ch < '0' || ch > '9') && ch != '-' {
			return false
		}
	}
	return true
}
