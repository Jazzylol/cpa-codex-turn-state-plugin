package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Store struct {
	mu    sync.RWMutex
	path  string
	rules map[string]Rule
}

func validateRule(r Rule) error {
	if r.AuthID == "" || len(r.AuthID) > 1024 || strings.TrimSpace(r.AuthID) != r.AuthID {
		return errors.New("auth_id is required and must be at most 1024 bytes")
	}
	if r.AuthIndex == "" || len(r.AuthIndex) > 128 || strings.TrimSpace(r.AuthIndex) != r.AuthIndex {
		return errors.New("auth_index is required and must be at most 128 bytes")
	}
	if len(r.Value) > MaxValueBytes {
		return fmt.Errorf("header value exceeds %d bytes", MaxValueBytes)
	}
	if r.Enabled && strings.TrimSpace(r.Value) == "" {
		return errors.New("an enabled rule requires a nonempty header value")
	}
	if strings.TrimSpace(r.Value) != r.Value {
		return errors.New("header value must not have leading or trailing whitespace")
	}
	for _, c := range []byte(r.Value) {
		if c < 0x20 || c > 0x7e {
			return errors.New("header value must contain printable ASCII without line breaks")
		}
	}
	return nil
}

func (s *Store) Configure(raw []byte) error {
	var req lifecycleRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return errors.New("invalid lifecycle request")
	}
	var cfg config
	if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
		return errors.New("invalid plugin YAML configuration")
	}
	if strings.TrimSpace(cfg.StateFile) == "" {
		cfg.StateFile = filepath.Join("plugins", ID+"-state.json")
	}
	path, err := filepath.Abs(cfg.StateFile)
	if err != nil {
		return errors.New("invalid state_file path")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == path && s.rules != nil {
		return nil
	}
	rules := make(map[string]Rule)
	f, err := os.Open(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("open state file: %w", err)
	}
	if err == nil {
		var saved state
		decoder := json.NewDecoder(io.LimitReader(f, 8<<20))
		errDecode := decoder.Decode(&saved)
		var extra any
		errExtra := decoder.Decode(&extra)
		errClose := f.Close()
		if errDecode != nil || errExtra != io.EOF || saved.Version != 1 {
			return errors.New("invalid state file; refusing to discard saved rules")
		}
		if errClose != nil {
			return fmt.Errorf("close state file: %w", errClose)
		}
		for _, r := range saved.Rules {
			if errValidate := validateRule(r); errValidate != nil {
				return fmt.Errorf("invalid saved rule: %w", errValidate)
			}
			if _, exists := rules[r.AuthID]; exists {
				return errors.New("duplicate auth_id in state file")
			}
			rules[r.AuthID] = r
		}
	}
	s.path = path
	s.rules = rules
	return nil
}

func (s *Store) List() []Rule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sortedRules(s.rules)
}

func sortedRules(rules map[string]Rule) []Rule {
	result := make([]Rule, 0, len(rules))
	for _, r := range rules {
		result = append(result, r)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].AuthID < result[j].AuthID })
	return result
}

func (s *Store) Match(id, index string) (Rule, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.rules[id]
	return r, ok && r.Enabled && r.AuthIndex == index
}

func (s *Store) Put(r Rule) error {
	if err := validateRule(r); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commitLocked(r.AuthID, &r)
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commitLocked(id, nil)
}

func (s *Store) commitLocked(id string, rule *Rule) error {
	if s.path == "" {
		return errors.New("plugin is not configured")
	}
	next := make(map[string]Rule, len(s.rules)+1)
	for k, v := range s.rules {
		next[k] = v
	}
	if rule == nil {
		delete(next, id)
	} else {
		next[id] = *rule
	}
	raw, err := json.MarshalIndent(state{Version: 1, Rules: sortedRules(next)}, "", "  ")
	if err != nil {
		return err
	}
	if len(raw) > 8<<20 {
		return errors.New("state file exceeds the 8 MiB reload limit")
	}
	if errMkdir := os.MkdirAll(filepath.Dir(s.path), 0700); errMkdir != nil {
		return fmt.Errorf("create state directory: %w", errMkdir)
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".turn-state-*")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
	}
	name := f.Name()
	cleanup := func() { _ = os.Remove(name) }
	defer cleanup()
	if _, errWrite := f.Write(raw); errWrite != nil {
		_ = f.Close()
		return fmt.Errorf("write state: %w", errWrite)
	}
	if errSync := f.Sync(); errSync != nil {
		_ = f.Close()
		return fmt.Errorf("sync state: %w", errSync)
	}
	if errClose := f.Close(); errClose != nil {
		return fmt.Errorf("close state: %w", errClose)
	}
	if errRename := os.Rename(name, s.path); errRename != nil {
		return fmt.Errorf("replace state: %w", errRename)
	}
	s.rules = next
	return nil
}
