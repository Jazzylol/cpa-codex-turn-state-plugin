package plugin

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

//go:embed panel.html
var panel []byte

func jsonResponse(status int, v any) managementResponse {
	raw, _ := json.Marshal(v)
	return managementResponse{StatusCode: status, Headers: http.Header{"Content-Type": {"application/json; charset=utf-8"}, "Cache-Control": {"no-store"}, "X-Content-Type-Options": {"nosniff"}}, Body: raw}
}

func problem(status int, message string) managementResponse {
	return jsonResponse(status, map[string]string{"error": message})
}

func (a *App) host(method string, req any, result any) error {
	if a.CallHost == nil {
		return errors.New("host callbacks unavailable")
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	response, errCall := a.CallHost(method, raw)
	if errCall != nil {
		return errors.New("host callback failed")
	}
	var env envelope
	if errDecode := json.Unmarshal(response, &env); errDecode != nil || !env.OK {
		return errors.New("host callback rejected request")
	}
	if errDecode := json.Unmarshal(env.Result, result); errDecode != nil {
		return errors.New("invalid host callback response")
	}
	return nil
}

func (a *App) credentials(callback string) ([]Credential, error) {
	var result struct {
		Files []Credential `json:"files"`
	}
	if err := a.host("host.auth.list", map[string]string{"host_callback_id": callback}, &result); err != nil {
		return nil, err
	}
	filtered := make([]Credential, 0)
	for _, c := range result.Files {
		if c.Provider == "codex" && c.ID != "" && c.AuthIndex != "" && !c.RuntimeOnly {
			filtered = append(filtered, c)
		}
	}
	return filtered, nil
}

func (a *App) nativeHeaderConflict(index, callback string) (bool, error) {
	var result struct {
		JSON json.RawMessage `json:"json"`
	}
	if err := a.host("host.auth.get", map[string]string{"auth_index": index, "host_callback_id": callback}, &result); err != nil {
		return false, err
	}
	var auth struct {
		Headers map[string]any `json:"headers"`
	}
	if err := json.Unmarshal(result.JSON, &auth); err != nil {
		return false, errors.New("cannot inspect credential header settings")
	}
	for key, value := range auth.Headers {
		if v, ok := value.(string); ok && strings.EqualFold(strings.TrimSpace(key), Header) && strings.TrimSpace(v) != "" {
			return true, nil
		}
	}
	return false, nil
}

func (a *App) Management(req managementRequest) managementResponse {
	if req.Method == "GET" && req.Path == ResourcePath {
		return managementResponse{StatusCode: 200, Headers: http.Header{"Content-Type": {"text/html; charset=utf-8"}, "Cache-Control": {"no-store"}, "X-Content-Type-Options": {"nosniff"}, "Referrer-Policy": {"no-referrer"}, "Content-Security-Policy": {"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'self'; base-uri 'none'; form-action 'none'"}}, Body: panel}
	}
	switch {
	case req.Method == "GET" && req.Path == APIPrefix+"/credentials":
		items, err := a.credentials(req.HostCallbackID)
		if err != nil {
			return problem(502, "无法读取 CPA 凭据列表")
		}
		return jsonResponse(200, map[string]any{"credentials": items})
	case req.Method == "GET" && req.Path == APIPrefix+"/rules":
		return jsonResponse(200, map[string]any{"rules": a.Store.List(), "version": Version})
	case (req.Method == "PUT" || req.Method == "DELETE") && req.Path == APIPrefix+"/rule":
		if len(req.Body) > 16<<10 {
			return problem(413, "配置内容过大")
		}
		var rule Rule
		dec := json.NewDecoder(bytes.NewReader(req.Body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&rule); err != nil {
			return problem(400, "配置 JSON 格式不正确")
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			return problem(400, "只允许一个 JSON 对象")
		}
		if req.Method == "DELETE" {
			if rule.AuthID == "" {
				return problem(400, "请选择凭据")
			}
			if err := a.Store.Delete(rule.AuthID); err != nil {
				return problem(500, "无法保存，请检查 state_file 目录权限")
			}
			return jsonResponse(200, map[string]bool{"deleted": true})
		}
		if err := validateRule(rule); err != nil {
			return problem(400, err.Error())
		}
		items, err := a.credentials(req.HostCallbackID)
		if err != nil {
			return problem(502, "无法核对 CPA 凭据")
		}
		found := false
		for _, item := range items {
			if item.ID == rule.AuthID && item.AuthIndex == rule.AuthIndex {
				found = true
				break
			}
		}
		if !found {
			return problem(404, "凭据已变更或不存在，请刷新列表后重新选择")
		}
		if rule.Enabled {
			conflict, errCheck := a.nativeHeaderConflict(rule.AuthIndex, req.HostCallbackID)
			if errCheck != nil {
				return problem(409, "无法核对凭据的原生请求头设置，未启用规则")
			}
			if conflict {
				return problem(409, "该凭据已在原生 headers 中配置 X-Codex-Turn-State，请先移除重复配置")
			}
		}
		if errSave := a.Store.Put(rule); errSave != nil {
			return problem(500, "无法保存，请检查 state_file 目录权限")
		}
		return jsonResponse(200, map[string]any{"rule": rule})
	default:
		return problem(404, "接口不存在")
	}
}
