package integration

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/net/http2"

	"cpa-codex-turn-state/internal/plugin"
)

type observation struct {
	Account   string `json:"account"`
	Transport string `json:"transport"`
	Path      string `json:"path"`
	Value     string `json:"value"`
	Count     int    `json:"header_count"`
}

type upstream struct {
	mu    sync.Mutex
	seen  []observation
	failA atomic.Bool
	seq   atomic.Int64
}

func (u *upstream) snapshot() []observation {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]observation(nil), u.seen...)
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	account := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer mock-")
	transport := "http"
	if websocket.IsWebSocketUpgrade(r) {
		transport = "websocket"
	}
	u.mu.Lock()
	u.seen = append(u.seen, observation{account, transport, r.URL.Path, r.Header.Get(plugin.Header), len(r.Header.Values(plugin.Header))})
	u.mu.Unlock()
	if account == "a" && u.failA.Load() {
		w.WriteHeader(502)
		_, _ = w.Write([]byte(`{"error":{"message":"injected upstream failure","type":"server_error"}}`))
		return
	}
	if transport == "websocket" {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, raw, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			for _, event := range u.events(raw) {
				if errWrite := conn.WriteJSON(event); errWrite != nil {
					return
				}
			}
		}
	}
	body, _ := io.ReadAll(r.Body)
	if strings.Contains(r.URL.Path, "/images/") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"bW9jay1pbWFnZQ=="}]}`))
		return
	}
	if strings.HasSuffix(r.URL.Path, "/compact") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"compact_mock","object":"response.compaction","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range u.events(body) {
		raw, _ := json.Marshal(event)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func (u *upstream) events(raw []byte) []map[string]any {
	var req map[string]any
	_ = json.Unmarshal(raw, &req)
	id := fmt.Sprintf("resp_mock_%d", u.seq.Add(1))
	response := map[string]any{"id": id, "object": "response", "model": req["model"], "status": "completed", "output": []any{map[string]any{"id": "msg_mock", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "OK", "annotations": []any{}}}}}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}
	return []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": id, "object": "response", "model": req["model"], "status": "in_progress", "output": []any{}}},
		{"type": "response.output_text.delta", "item_id": "msg_mock", "output_index": 0, "content_index": 0, "delta": "OK"},
		{"type": "response.completed", "response": response},
	}
}

type closedConn struct {
	net.Conn
	once sync.Once
	done chan struct{}
}

func (c *closedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.done) })
	return err
}

type oneListener struct {
	conn  *closedConn
	first bool
}

func (l *oneListener) Accept() (net.Conn, error) {
	if !l.first {
		l.first = true
		return l.conn, nil
	}
	<-l.conn.done
	return nil, net.ErrClosed
}
func (l *oneListener) Close() error   { return l.conn.Close() }
func (l *oneListener) Addr() net.Addr { return l.conn.LocalAddr() }

func mockProxy(t *testing.T, u *upstream, dir string) *httptest.Server {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "chatgpt.com"}, DNSNames: []string{"chatgpt.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Host != "chatgpt.com:443" {
			http.Error(w, "only the local Codex fixture is allowed", 502)
			return
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		_, _ = buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buf.Flush()
		tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}})
		if err := tlsConn.Handshake(); err != nil {
			_ = conn.Close()
			return
		}
		if tlsConn.ConnectionState().NegotiatedProtocol == "h2" {
			(&http2.Server{}).ServeConn(tlsConn, &http2.ServeConnOpts{Handler: u})
			return
		}
		listener := &oneListener{conn: &closedConn{Conn: tlsConn, done: make(chan struct{})}}
		server := &http.Server{Handler: u}
		_ = server.Serve(listener)
	}))
	t.Cleanup(proxy.Close)
	return proxy
}

func TestDockerPlugin(t *testing.T) {
	if os.Getenv("CPA_PLUGIN_INTEGRATION") != "1" {
		t.Skip("set CPA_PLUGIN_INTEGRATION=1 to run isolated Docker verification")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "validation"), 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(filepath.Join(root, "validation"), "runtime-")
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"auths", "plugins", "logs"} {
		if err := os.MkdirAll(filepath.Join(dir, part), 0700); err != nil {
			t.Fatal(err)
		}
	}
	lib, err := os.ReadFile(filepath.Join(root, "dist", "codex-turn-state.so"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugins", "codex-turn-state.so"), lib, 0600); err != nil {
		t.Fatal(err)
	}
	u := &upstream{}
	proxy := mockProxy(t, u, dir)
	for _, account := range []string{"a", "b"} {
		priority := 1
		if account == "a" {
			priority = 2
		}
		auth := map[string]any{"type": "codex", "plan_type": "team", "email": account + "@example.invalid", "access_token": "mock-" + account, "account_id": "mock-" + account, "expired": "2099-01-01T00:00:00Z", "last_refresh": "2098-01-01T00:00:00Z", "proxy_url": proxy.URL, "prefix": account, "priority": priority, "websockets": true}
		raw, _ := json.Marshal(auth)
		if err := os.WriteFile(filepath.Join(dir, "auths", account+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	config := fmt.Sprintf("host: 127.0.0.1\nport: %d\nauth-dir: /probe/auths\nrequest-log: true\nlogging-to-file: true\nrequest-retry: 0\nmax-retry-credentials: 2\nremote-management:\n  disable-control-panel: true\nplugins:\n  enabled: true\n  dir: /probe/plugins\n  configs:\n    codex-turn-state:\n      enabled: true\n      priority: 100\n      state_file: /probe/state.json\n", port)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	container := fmt.Sprintf("cpa-turn-state-plugin-test-%d", port)
	image := os.Getenv("CPA_TEST_IMAGE")
	if image == "" {
		image = "jinshenganyuci/cli-proxy-api:codex-identity-v7.3.4.1"
	}
	command := exec.Command("docker", "run", "-d", "--pull=never", "--name", container, "--network", "host", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--env", "MANAGEMENT_PASSWORD", "--env", "SSL_CERT_FILE=/probe/ca.pem", "--mount", "type=bind,src="+dir+",dst=/probe", "--mount", "type=bind,src="+filepath.Join(dir, "logs")+",dst=/CLIProxyAPI/logs", image, "./CLIProxyAPI", "--config", "/probe/config.yaml", "--local-model")
	command.Env = append(os.Environ(), "MANAGEMENT_PASSWORD=isolated-plugin-test-key")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("start test container: %v %s", err, out)
	}
	t.Cleanup(func() {
		out, _ := exec.Command("docker", "logs", container).CombinedOutput()
		_ = os.WriteFile(filepath.Join(root, "validation", "container.log"), out, 0600)
		_ = exec.Command("docker", "rm", "-f", container).Run()
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	call := func(method, path string, body any, authorized bool) (int, []byte) {
		var reader io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		}
		req, _ := http.NewRequest(method, base+path, reader)
		req.Header.Set("Content-Type", "application/json")
		if authorized {
			req.Header.Set("Authorization", "Bearer isolated-plugin-test-key")
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, []byte(err.Error())
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}
	deadline := time.Now().Add(25 * time.Second)
	for {
		status, raw := call("GET", plugin.APIPrefix+"/credentials", nil, true)
		if status == 200 {
			break
		}
		if time.Now().After(deadline) {
			logs, _ := exec.Command("docker", "logs", container).CombinedOutput()
			t.Fatalf("plugin not ready: %d %s\n%s", status, raw, logs)
		}
		time.Sleep(100 * time.Millisecond)
	}
	modelDeadline := time.Now().Add(15 * time.Second)
	for {
		status, raw := call("GET", "/v1/models", nil, false)
		if status == 200 && bytes.Contains(raw, []byte(`"a/gpt-5.6-luna"`)) {
			break
		}
		if time.Now().After(modelDeadline) {
			t.Fatalf("test models not registered: %d %s", status, raw)
		}
		time.Sleep(100 * time.Millisecond)
	}

	status, raw := call("GET", plugin.APIPrefix+"/credentials", nil, true)
	if status != 200 {
		t.Fatal("credential callback failed")
	}
	var catalog struct {
		Credentials []plugin.Credential `json:"credentials"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Credentials) != 2 {
		t.Fatalf("expected 2 credentials, got %s", raw)
	}
	credentials := make(map[string]plugin.Credential)
	for _, c := range catalog.Credentials {
		credentials[c.ID] = c
	}
	set := func(t *testing.T, account, value string, enabled bool) {
		t.Helper()
		c := credentials[account+".json"]
		code, body := call("PUT", plugin.APIPrefix+"/rule", plugin.Rule{AuthID: c.ID, AuthIndex: c.AuthIndex, Enabled: enabled, Value: value}, true)
		if code != 200 {
			t.Fatalf("save rule %s: %d %s", account, code, body)
		}
	}
	assertObservation := func(t *testing.T, from int, account, transport, value string) {
		t.Helper()
		seen := u.snapshot()
		if len(seen) <= from {
			t.Fatal("request never reached mock upstream")
		}
		last := seen[len(seen)-1]
		if last.Account != account || last.Transport != transport || last.Value != value {
			t.Fatalf("upstream mismatch: %+v", last)
		}
		if value != "" && last.Count != 1 {
			t.Fatalf("duplicate header: %+v", last)
		}
	}
	request := func(t *testing.T, path, model, incoming string, stream bool) {
		t.Helper()
		payload := map[string]any{"model": model, "input": "Reply OK", "stream": stream, "store": false}
		if path == "/v1/chat/completions" {
			payload = map[string]any{"model": model, "messages": []any{map[string]string{"role": "user", "content": "Reply OK"}}, "stream": stream}
		}
		body, _ := json.Marshal(payload)
		req, _ := http.NewRequest("POST", base+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if incoming != "" {
			req.Header.Set(plugin.Header, incoming)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("model request %s: %d %s", path, resp.StatusCode, raw)
		}
	}
	wsRequest := func(t *testing.T, model, incoming string) {
		t.Helper()
		headers := http.Header{}
		if incoming != "" {
			headers.Set(plugin.Header, incoming)
		}
		conn, resp, err := websocket.DefaultDialer.Dial(strings.Replace(base, "http://", "ws://", 1)+"/v1/responses", headers)
		if err != nil {
			t.Fatalf("downstream websocket failed: %v %v", err, resp)
		}
		defer conn.Close()
		if err := conn.WriteJSON(map[string]any{"type": "response.create", "model": model, "input": []any{map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": "Reply OK"}}}}, "store": false}); err != nil {
			t.Fatal(err)
		}
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				t.Fatal(err)
			}
			var event map[string]any
			_ = json.Unmarshal(message, &event)
			if event["type"] == "error" {
				t.Fatalf("websocket error: %s", message)
			}
			if event["type"] == "response.completed" {
				return
			}
		}
	}
	t.Run("management authentication and static page", func(t *testing.T) {
		for _, route := range []string{"/credentials", "/rules"} {
			status, _ := call("GET", plugin.APIPrefix+route, nil, false)
			if status != 401 && status != 403 {
				t.Fatalf("unprotected management endpoint %s: %d", route, status)
			}
		}
		status, body := call("GET", plugin.ResourcePath, nil, false)
		if status != 200 || !bytes.Contains(body, []byte("选择 Codex 凭据")) {
			t.Fatalf("resource page unavailable: %d %s", status, body)
		}
	})
	t.Run("default inactive", func(t *testing.T) {
		before := len(u.snapshot())
		request(t, "/v1/responses", "a/gpt-5.6-luna", "client-original", true)
		assertObservation(t, before, "a", "http", "client-original")
	})
	set(t, "a", "configured-a/+=&<literal>", true)
	t.Run("HTTP streaming override", func(t *testing.T) {
		before := len(u.snapshot())
		request(t, "/v1/responses", "a/gpt-5.6-luna", "client-old", true)
		assertObservation(t, before, "a", "http", "configured-a/+=&<literal>")
	})
	t.Run("HTTP nonstream override", func(t *testing.T) {
		before := len(u.snapshot())
		request(t, "/v1/responses", "a/gpt-5.6-luna", "", false)
		assertObservation(t, before, "a", "http", "configured-a/+=&<literal>")
	})
	t.Run("chat completions override", func(t *testing.T) {
		before := len(u.snapshot())
		request(t, "/v1/chat/completions", "a/gpt-5.6-luna", "client-old", true)
		assertObservation(t, before, "a", "http", "configured-a/+=&<literal>")
	})
	t.Run("compact override", func(t *testing.T) {
		before := len(u.snapshot())
		request(t, "/v1/responses/compact", "a/gpt-5.6-luna", "client-old", false)
		assertObservation(t, before, "a", "http", "configured-a/+=&<literal>")
	})
	t.Run("unsupported image route stops before upstream", func(t *testing.T) {
		before := len(u.snapshot())
		status, raw := call("POST", "/v1/images/generations", map[string]any{"model": "gpt-image-2.5", "prompt": "fixture", "stream": false}, false)
		if status != 501 || !bytes.Contains(raw, []byte("turn_state_image_transport_unsupported")) {
			t.Fatalf("image request: %d %s", status, raw)
		}
		if len(u.snapshot()) != before {
			t.Fatal("unsupported image request reached upstream without the configured state")
		}
	})
	t.Run("unconfigured credential isolation", func(t *testing.T) {
		before := len(u.snapshot())
		request(t, "/v1/responses", "b/gpt-5.6-luna", "client-b", true)
		assertObservation(t, before, "b", "http", "client-b")
	})
	t.Run("WebSocket handshake override", func(t *testing.T) {
		before := len(u.snapshot())
		wsRequest(t, "a/gpt-5.6-luna", "client-ws")
		assertObservation(t, before, "a", "websocket", "configured-a/+=&<literal>")
	})
	t.Run("WebSocket unconfigured credential", func(t *testing.T) {
		before := len(u.snapshot())
		wsRequest(t, "b/gpt-5.6-luna", "client-ws-b")
		assertObservation(t, before, "b", "websocket", "client-ws-b")
	})
	set(t, "b", "configured-b", true)
	t.Run("second credential own rule", func(t *testing.T) {
		before := len(u.snapshot())
		request(t, "/v1/responses", "b/gpt-5.6-luna", "client-b", true)
		assertObservation(t, before, "b", "http", "configured-b")
	})
	set(t, "b", "configured-b", false)
	t.Run("credential failover", func(t *testing.T) {
		u.failA.Store(true)
		defer u.failA.Store(false)
		before := len(u.snapshot())
		request(t, "/v1/responses", "gpt-5.6-luna", "client-failover", true)
		seen := u.snapshot()[before:]
		if len(seen) != 2 || seen[0].Account != "a" || seen[0].Value != "configured-a/+=&<literal>" || seen[1].Account != "b" || seen[1].Value != "client-failover" {
			t.Fatalf("credential failover did not isolate headers: %+v", seen)
		}
	})
	t.Run("disable restores client behavior", func(t *testing.T) {
		set(t, "b", "configured-b", false)
		before := len(u.snapshot())
		request(t, "/v1/responses", "b/gpt-5.6-luna", "client-restored", true)
		assertObservation(t, before, "b", "http", "client-restored")
	})
	t.Run("delete rule", func(t *testing.T) {
		status, body := call("DELETE", plugin.APIPrefix+"/rule", map[string]string{"auth_id": "b.json"}, true)
		if status != 200 {
			t.Fatalf("delete: %d %s", status, body)
		}
		before := len(u.snapshot())
		request(t, "/v1/responses", "b/gpt-5.6-luna", "", true)
		assertObservation(t, before, "b", "http", "")
	})
	data, _ := json.MarshalIndent(u.snapshot(), "", "  ")
	_ = os.WriteFile(filepath.Join(root, "validation", "upstream-observations.json"), data, 0600)
	if os.Getenv("CPA_BROWSER_MODULE") != "" {
		t.Run("browser management workflow", func(t *testing.T) {
			command := exec.Command("node", filepath.Join(root, "tests", "browser.cjs"))
			command.Env = append(os.Environ(), "CPA_TEST_URL="+base, "CPA_TEST_ROOT="+root)
			if out, err := command.CombinedOutput(); err != nil {
				t.Fatalf("browser checks: %v\n%s", err, out)
			} else {
				t.Log(string(out))
			}
		})
	}
	for _, account := range []string{"a", "b"} {
		raw, err := os.ReadFile(filepath.Join(dir, "auths", account+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(plugin.Header)) {
			t.Fatal("plugin modified credential headers")
		}
	}
}
