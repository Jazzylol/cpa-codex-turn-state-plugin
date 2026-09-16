package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func configuredApp(t *testing.T) *App {
	t.Helper()
	a := NewApp(nil)
	configureAt(t, a, filepath.Join(t.TempDir(), "state.json"))
	return a
}

func configureAt(t *testing.T, a *App, path string) {
	t.Helper()
	raw, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte("state_file: " + path + "\n")})
	if _, err := a.Handle("plugin.register", raw); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialIsolationAndLiteralHeader(t *testing.T) {
	a := configuredApp(t)
	if err := a.Store.Put(Rule{AuthID: "a.json", AuthIndex: "index-a", Enabled: true, Value: "$literal/+/=&<test>"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, format, id, index string
		want                    bool
	}{
		{"selected", "codex", "a.json", "index-a", true},
		{"compact", "openai-response", "a.json", "index-a", true},
		{"images", "openai-image", "a.json", "index-a", false},
		{"other credential", "codex", "b.json", "index-b", false},
		{"stale index", "codex", "a.json", "stale", false},
		{"missing identity", "codex", "", "", false},
		{"other provider", "claude", "a.json", "index-a", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{"x-codex-turn-state": {"client"}, "X-CODEX-TURN-STATE": {"duplicate"}, "X-Other": {"preserved"}}
			before := headers.Clone()
			r := a.Intercept(interceptRequest{ToFormat: tc.format, Headers: headers, Metadata: map[string]any{"selected_auth_id": tc.id, "selected_auth_index": tc.index}})
			if got := r.Headers.Get(Header); (got != "") != tc.want {
				t.Fatalf("unexpected override presence %v", got != "")
			}
			if tc.want && r.Headers.Get(Header) != "$literal/+/=&<test>" {
				t.Fatal("header was expanded or escaped")
			}
			if tc.want && len(r.ClearHeaders) != 3 {
				t.Fatal("mixed-case duplicates were not cleared")
			}
			if !tc.want && len(r.ClearHeaders) != 0 {
				t.Fatal("unrelated credential was changed")
			}
			if tc.name == "images" && (!r.Terminate || r.StatusCode != 501) {
				t.Fatal("unsupported image route must stop before upstream")
			}
			if !reflect.DeepEqual(headers, before) {
				t.Fatal("input headers mutated")
			}
		})
	}
	for _, enabled := range []bool{false, true, false} {
		if err := a.Store.Put(Rule{AuthID: "a.json", AuthIndex: "index-a", Enabled: enabled, Value: "new"}); err != nil {
			t.Fatal(err)
		}
		_, ok := a.Store.Match("a.json", "index-a")
		if ok != enabled {
			t.Fatal("toggle did not take effect")
		}
	}
}

func TestValueValidationAndAtomicFailure(t *testing.T) {
	a := configuredApp(t)
	base := Rule{AuthID: "a.json", AuthIndex: "i", Enabled: true, Value: "good"}
	if err := a.Store.Put(base); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", " leading", "trailing ", "bad\r\nInjected: yes", "bad\x00", "非ASCII", strings.Repeat("a", MaxValueBytes+1)} {
		r := base
		r.Value = value
		if err := a.Store.Put(r); err == nil {
			t.Fatal("invalid header accepted")
		}
		if got, _ := a.Store.Match("a.json", "i"); got.Value != "good" {
			t.Fatal("failed validation replaced rule")
		}
	}
	if err := os.Remove(a.Store.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(a.Store.path, 0700); err != nil {
		t.Fatal(err)
	}
	r := base
	r.Value = "must-not-be-applied"
	if err := a.Store.Put(r); err == nil {
		t.Fatal("expected persistent save failure")
	}
	if got, _ := a.Store.Match("a.json", "i"); got.Value != "good" {
		t.Fatal("disk failure changed live rule")
	}
}

func TestStateSurvivesReloadWithoutCredentialWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "state.json")
	a := NewApp(nil)
	configureAt(t, a, path)
	rule := Rule{AuthID: "a.json", AuthIndex: "i", Enabled: true, Value: "value"}
	if err := a.Store.Put(rule); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("state permissions = %o", info.Mode().Perm())
	}
	b := NewApp(nil)
	configureAt(t, b, path)
	if got, ok := b.Store.Match("a.json", "i"); !ok || got != rule {
		t.Fatal("rule lost on restart")
	}
	if err := b.Store.Delete("a.json"); err != nil {
		t.Fatal(err)
	}
	c := NewApp(nil)
	configureAt(t, c, path)
	if _, ok := c.Store.Match("a.json", "i"); ok {
		t.Fatal("deleted rule restored")
	}
}

func TestConcurrentReadAndSave(t *testing.T) {
	a := configuredApp(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 12; j++ {
				if n == 0 {
					if err := a.Store.Put(Rule{AuthID: "a", AuthIndex: "i", Enabled: true, Value: fmt.Sprint(j)}); err != nil {
						t.Error(err)
					}
				} else {
					a.Store.Match("a", "i")
					a.Store.List()
				}
			}
		}(i)
	}
	wg.Wait()
}

func hostFixture(t *testing.T, conflict bool) HostCall {
	t.Helper()
	return func(method string, raw []byte) ([]byte, error) {
		switch method {
		case "host.auth.list":
			return okEnvelope(map[string]any{"files": []map[string]any{
				{"id": "a.json", "auth_index": "i", "name": "A", "provider": "codex", "access_token": "never-return-this", "path": "/private/auths/a.json"},
				{"id": "b.json", "auth_index": "b", "provider": "claude"},
				{"id": "c", "auth_index": "c", "provider": "codex", "runtime_only": true},
			}})
		case "host.auth.get":
			headers := map[string]string{}
			if conflict {
				headers["x-codex-turn-state"] = "native"
			}
			return okEnvelope(map[string]any{"json": map[string]any{"access_token": "never-return-this", "headers": headers}})
		default:
			t.Fatalf("unexpected callback %s", method)
			return nil, nil
		}
	}
}

func TestManagementProjectionCRUDAndNativeConflict(t *testing.T) {
	a := configuredApp(t)
	a.CallHost = hostFixture(t, false)
	catalog := a.Management(managementRequest{Method: "GET", Path: APIPrefix + "/credentials"})
	if catalog.StatusCode != 200 || strings.Contains(string(catalog.Body), "never-return-this") || strings.Contains(string(catalog.Body), "private") || strings.Contains(string(catalog.Body), "b.json") {
		t.Fatal("unsafe or incorrect credential projection")
	}
	if r := a.Management(managementRequest{Method: "GET", Path: ResourcePath}); r.StatusCode != 200 || strings.Contains(string(r.Body), "never-return-this") {
		t.Fatal("resource must contain only static UI")
	}
	rule := Rule{AuthID: "a.json", AuthIndex: "i", Enabled: true, Value: "literal<&>"}
	put := func(rule Rule) managementResponse {
		body, _ := json.Marshal(rule)
		return a.Management(managementRequest{Method: "PUT", Path: APIPrefix + "/rule", Body: body})
	}
	if r := put(rule); r.StatusCode != 200 {
		t.Fatalf("put status %d: %s", r.StatusCode, r.Body)
	}
	rule.AuthIndex = "stale"
	if r := put(rule); r.StatusCode != 404 {
		t.Fatal("stale credential accepted")
	}
	rule.AuthIndex = "i"
	a.CallHost = hostFixture(t, true)
	if r := put(rule); r.StatusCode != 409 {
		t.Fatal("native header conflict was not reported")
	}
	rule.Enabled = false
	if r := put(rule); r.StatusCode != 200 {
		t.Fatal("must allow disabling conflicted rule")
	}
	bad := a.Management(managementRequest{Method: "PUT", Path: APIPrefix + "/rule", Body: []byte(`{"auth_id":"a.json","surprise":true}`)})
	if bad.StatusCode != 400 {
		t.Fatal("unknown field accepted")
	}
	if r := a.Management(managementRequest{Method: "DELETE", Path: APIPrefix + "/rule", Body: []byte(`{"auth_id":"a.json"}`)}); r.StatusCode != 200 {
		t.Fatal("delete failed")
	}
	if len(a.Store.List()) != 0 {
		t.Fatal("rule remained after delete")
	}
}

func TestInvalidStateFailsClosed(t *testing.T) {
	a := configuredApp(t)
	path := filepath.Join(t.TempDir(), "bad.json")
	for _, raw := range []string{`{`, `{"version":9,"rules":[]}`, `{"version":1,"rules":[]} {}`, `{"version":1,"rules":[{"auth_id":"a","auth_index":"i","enabled":true,"value":"bad\n"}]}`} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		request, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte("state_file: " + path)})
		if _, err := a.Handle("plugin.reconfigure", request); err == nil {
			t.Fatal("invalid saved state accepted")
		}
	}
}
