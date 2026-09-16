package plugin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type App struct {
	Store    Store
	CallHost HostCall
}

func NewApp(host HostCall) *App { return &App{CallHost: host} }

func (a *App) Handle(method string, raw []byte) (out []byte, err error) {
	defer func() {
		if recover() != nil {
			out = nil
			err = errors.New("plugin call failed")
		}
	}()
	switch method {
	case "plugin.register", "plugin.reconfigure":
		if errConfigure := a.Store.Configure(raw); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(map[string]any{
			"schema_version": 6,
			"metadata":       map[string]any{"Name": "Codex Turn State", "Version": Version, "Author": "jinshenganyuci", "GitHubRepository": "https://github.com/jinshenganyuci/cpa-codex-turn-state-plugin", "ConfigFields": []map[string]string{{"Name": "state_file", "Type": "string", "Description": "Private JSON rule file. Default: plugins/codex-turn-state-state.json; relative to CPA working directory."}}},
			"capabilities":   map[string]bool{"request_interceptor": true, "management_api": true},
		})
	case "request.intercept_before":
		return okEnvelope(interceptResponse{})
	case "request.intercept_after":
		var req interceptRequest
		if errDecode := json.Unmarshal(raw, &req); errDecode != nil {
			return nil, errors.New("invalid interceptor request")
		}
		return okEnvelope(a.Intercept(req))
	case "management.register":
		return okEnvelope(map[string]any{
			"routes": []map[string]string{
				{"Method": "GET", "Path": APIPrefix + "/credentials"},
				{"Method": "GET", "Path": APIPrefix + "/rules"},
				{"Method": "PUT", "Path": APIPrefix + "/rule"},
				{"Method": "DELETE", "Path": APIPrefix + "/rule"},
			},
			"resources": []map[string]string{{"Path": "/panel", "Menu": "Codex Turn State", "Description": "按凭据设置 X-Codex-Turn-State"}},
		})
	case "management.handle":
		var req managementRequest
		if errDecode := json.Unmarshal(raw, &req); errDecode != nil {
			return nil, errors.New("invalid management request")
		}
		return okEnvelope(a.Management(req))
	case "plugin.quiesce", "plugin.shutdown":
		return okEnvelope(struct{}{})
	default:
		return nil, errors.New("unsupported plugin method")
	}
}

// Intercept runs after selection for each execution attempt. It never mutates
// credential files, routing decisions, input headers, or unrelated credentials.
func (a *App) Intercept(req interceptRequest) interceptResponse {
	// Codex compaction uses the openai-response target format. Credential ID
	// and index still identify the Codex file selected when the rule was saved.
	if !strings.EqualFold(req.ToFormat, "codex") && !strings.EqualFold(req.ToFormat, "openai-response") && !strings.EqualFold(req.ToFormat, "openai-image") {
		return interceptResponse{}
	}
	id, _ := req.Metadata["selected_auth_id"].(string)
	index, _ := req.Metadata["selected_auth_index"].(string)
	rule, ok := a.Store.Match(id, index)
	if !ok {
		return interceptResponse{}
	}
	// Current CPA image executors reconstruct headers from the original Gin
	// request, bypassing opts.Headers. Do not silently send a different state.
	if strings.EqualFold(req.ToFormat, "openai-image") {
		return interceptResponse{Terminate: true, StatusCode: http.StatusNotImplemented, ResponseBody: []byte(`{"error":{"code":"turn_state_image_transport_unsupported","message":"This CPA image route ignores plugin header overrides. Disable this credential's turn-state rule before using the image route, or use a host with image header interception support.","type":"invalid_request_error"}}`)}
	}
	clear := []string{Header}
	for key := range req.Headers {
		if key != Header && strings.EqualFold(key, Header) {
			clear = append(clear, key)
		}
	}
	return interceptResponse{Headers: http.Header{Header: []string{rule.Value}}, ClearHeaders: clear}
}
