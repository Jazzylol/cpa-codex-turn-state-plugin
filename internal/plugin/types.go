package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
)

const (
	ID            = "codex-turn-state"
	Header        = "X-Codex-Turn-State"
	APIPrefix     = "/v0/management/plugins/" + ID
	ResourcePath  = "/v0/resource/plugins/" + ID + "/panel"
	MaxValueBytes = 8192
)

var Version = "0.1.0"

type HostCall func(string, []byte) ([]byte, error)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Rule struct {
	AuthID    string `json:"auth_id"`
	AuthIndex string `json:"auth_index"`
	Enabled   bool   `json:"enabled"`
	Value     string `json:"value"`
}

type state struct {
	Version int    `json:"version"`
	Rules   []Rule `json:"rules"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type config struct {
	StateFile string `yaml:"state_file"`
}

type interceptRequest struct {
	ToFormat string
	Headers  http.Header
	Metadata map[string]any
}

type interceptResponse struct {
	Headers      http.Header `json:",omitempty"`
	ClearHeaders []string    `json:",omitempty"`
	Terminate    bool        `json:",omitempty"`
	StatusCode   int         `json:",omitempty"`
	ResponseBody []byte      `json:",omitempty"`
}

type managementRequest struct {
	Method         string
	Path           string
	Query          url.Values
	Headers        http.Header
	Body           []byte
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type managementResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// Credential deliberately excludes tokens, paths, and unrelated host metadata.
type Credential struct {
	ID          string `json:"id"`
	AuthIndex   string `json:"auth_index"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Label       string `json:"label"`
	Disabled    bool   `json:"disabled"`
	RuntimeOnly bool   `json:"runtime_only"`
	Status      string `json:"status"`
}

func okEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func ErrorEnvelope(message string) []byte {
	raw, _ := json.Marshal(envelope{Error: &rpcError{Code: "plugin_error", Message: message}})
	return raw
}
