package internal

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// adminAuth 检查管理后台访问凭据。
// 优先级：环境变量 AUTH_TOKEN[0] → ApiKeyManager 中的 key
// 支持 Bearer header / X-Admin-Key header / ?key=xxx / cookie
func adminAuth(r *http.Request) bool {
	if Cfg.SkipAuthToken {
		return true
	}
	candidates := []string{}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		candidates = append(candidates, strings.TrimPrefix(auth, "Bearer "))
	}
	if k := r.Header.Get("X-Admin-Key"); k != "" {
		candidates = append(candidates, k)
	}
	if k := r.URL.Query().Get("key"); k != "" {
		candidates = append(candidates, k)
	}
	if c, err := r.Cookie("admin_key"); err == nil {
		candidates = append(candidates, c.Value)
	}
	for _, k := range candidates {
		if k == "" {
			continue
		}
		// env-based admin key（第一个 AUTH_TOKEN）
		if len(Cfg.AuthTokens) > 0 && subtle.ConstantTimeCompare([]byte(k), []byte(Cfg.AuthTokens[0])) == 1 {
			return true
		}
		// 用户管理的 key 也允许登录后台
		if GetApiKeyManager().Validate(k) {
			return true
		}
	}
	return false
}

func adminWriteJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// HandleAdminLogin POST /admin/api/login
// body: {"key": "..."}, 校验后种 cookie
func HandleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		adminWriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		adminWriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if Cfg.SkipAuthToken {
		setAdminCookie(w, "skip")
		adminWriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	// env AUTH_TOKEN[0] 或用户创建的 API Key 都可以登录后台
	envOK := len(Cfg.AuthTokens) > 0 && subtle.ConstantTimeCompare([]byte(body.Key), []byte(Cfg.AuthTokens[0])) == 1
	managedOK := GetApiKeyManager().Validate(body.Key)
	if !envOK && !managedOK {
		adminWriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "无效的密钥"})
		return
	}
	setAdminCookie(w, body.Key)
	adminWriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func setAdminCookie(w http.ResponseWriter, key string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "admin_key",
		Value:    key,
		Path:     "/admin",
		HttpOnly: true,
		MaxAge:   30 * 24 * 3600,
		SameSite: http.SameSiteLaxMode,
	})
}

// HandleAdminLogout
func HandleAdminLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:   "admin_key",
		Value:  "",
		Path:   "/admin",
		MaxAge: -1,
	})
	adminWriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// HandleAdminOverview GET /admin/api/overview
// 返回总体状态：telemetry + token 计数 + captcha provider 状态
func HandleAdminOverview(w http.ResponseWriter, r *http.Request) {
	if !adminAuth(r) {
		adminWriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	telemetry := GetTelemetryData()

	// Captcha Provider 状态
	captchaStatus := map[string]interface{}{
		"configured": Cfg.CaptchaProviderURL != "",
		"healthy":    false,
	}
	if Cfg.CaptchaProviderURL != "" {
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get(strings.TrimRight(Cfg.CaptchaProviderURL, "/") + "/health")
		if err == nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			var hd map[string]interface{}
			if json.Unmarshal(body, &hd) == nil {
				captchaStatus["healthy"] = resp.StatusCode == http.StatusOK
				captchaStatus["details"] = hd
			}
		}
	}

	// 模型 stats
	modelStats := make([]map[string]interface{}, 0, len(telemetry.ModelStats))
	for name, s := range telemetry.ModelStats {
		modelStats = append(modelStats, map[string]interface{}{
			"model":         name,
			"calls":         s.Requests,
			"input_tokens":  s.InputTok,
			"output_tokens": s.OutputTok,
		})
	}

	adminWriteJSON(w, http.StatusOK, map[string]interface{}{
		"version": "2.0.0",
		"telemetry": map[string]interface{}{
			"uptime":              telemetry.Uptime,
			"total_requests":      telemetry.TotalRequests,
			"rpm":                 telemetry.RPM,
			"total_input_tokens":  telemetry.TotalInputTok,
			"total_output_tokens": telemetry.TotalOutputTok,
			"avg_input_tokens":    telemetry.AvgInputTok,
			"avg_output_tokens":   telemetry.AvgOutputTok,
			"total_calls":         telemetry.TotalCalls,
			"success_calls":       telemetry.SuccessCalls,
			"success_rate":        telemetry.SuccessRate,
			"multimodal_calls":    telemetry.MultimodalCalls,
		},
		"tokens": map[string]interface{}{
			"managed_token_count": len(GetTokenManager().ListTokens()),
			"valid_token_count":   telemetry.ValidTokens,
		},
		"api_keys": map[string]interface{}{
			"total":      len(GetApiKeyManager().List()),
			"env_count":  len(Cfg.AuthTokens),
		},
		"captcha":     captchaStatus,
		"model_stats": modelStats,
	})
}

// HandleAdminConfig GET /admin/api/config
// 返回当前生效配置（脱敏 token）
func HandleAdminConfig(w http.ResponseWriter, r *http.Request) {
	if !adminAuth(r) {
		adminWriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	maskedAuth := make([]string, len(Cfg.AuthTokens))
	for i, t := range Cfg.AuthTokens {
		maskedAuth[i] = maskToken(t)
	}
	maskedBackup := make([]string, len(Cfg.BackupTokens))
	for i, t := range Cfg.BackupTokens {
		maskedBackup[i] = maskToken(t)
	}

	adminWriteJSON(w, http.StatusOK, map[string]interface{}{
		"port":                       Cfg.Port,
		"api_endpoint":               Cfg.APIEndpoint,
		"captcha_provider_url":       Cfg.CaptchaProviderURL,
		"browser_bridge_url":         Cfg.BrowserBridgeURL,
		"auth_tokens":                maskedAuth,
		"backup_tokens":              maskedBackup,
		"debug_logging":              Cfg.DebugLogging,
		"tool_support":               Cfg.ToolSupport,
		"force_tool_choice_required": Cfg.ForceToolChoiceRequired,
		"retry_count":                Cfg.RetryCount,
		"skip_auth_token":            Cfg.SkipAuthToken,
		"log_level":                  Cfg.LogLevel,
		"fe_version":                 GetFeVersion(),
	})
}

// HandleAdminTokens GET /admin/api/tokens
// 返回 TokenManager 管理的 z.ai JWT token（可增删）
func HandleAdminTokens(w http.ResponseWriter, r *http.Request) {
	if !adminAuth(r) {
		adminWriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	managed := GetTokenManager().ListTokens()
	managedTokens := make([]map[string]interface{}, 0, len(managed))
	for _, info := range managed {
		entry := map[string]interface{}{
			"token_full": info.Token,
			"masked":     maskToken(info.Token),
			"email":      info.Email,
			"user_id":    info.UserID,
			"valid":      info.Valid,
			"use_count":  info.UseCount,
		}
		if !info.LastChecked.IsZero() {
			entry["last_checked"] = info.LastChecked.Format(time.RFC3339)
		}
		managedTokens = append(managedTokens, entry)
	}

	adminWriteJSON(w, http.StatusOK, map[string]interface{}{
		"tokens": managedTokens,
		"total":  len(managedTokens),
	})
}

// HandleAdminTokenAdd POST /admin/api/tokens
// body: {"token": "eyJ..."}
func HandleAdminTokenAdd(w http.ResponseWriter, r *http.Request) {
	if !adminAuth(r) {
		adminWriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if r.Method != http.MethodPost {
		adminWriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		adminWriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	info, err := GetTokenManager().AddToken(body.Token)
	if err != nil {
		adminWriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	adminWriteJSON(w, http.StatusOK, map[string]interface{}{
		"ok":      true,
		"token":   maskToken(info.Token),
		"email":   info.Email,
		"user_id": info.UserID,
	})
}

// HandleAdminTokenDelete DELETE /admin/api/tokens
// body: {"token": "完整的 token"}
func HandleAdminTokenDelete(w http.ResponseWriter, r *http.Request) {
	if !adminAuth(r) {
		adminWriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		adminWriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		adminWriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if err := GetTokenManager().RemoveToken(body.Token); err != nil {
		adminWriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	adminWriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// HandleAdminTokenValidate POST /admin/api/tokens/validate
// body: {"token": "完整的 token"}
func HandleAdminTokenValidate(w http.ResponseWriter, r *http.Request) {
	if !adminAuth(r) {
		adminWriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if r.Method != http.MethodPost {
		adminWriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		adminWriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	valid, err := GetTokenManager().ValidateTokenNow(body.Token)
	if err != nil {
		adminWriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	adminWriteJSON(w, http.StatusOK, map[string]bool{"ok": true, "valid": valid})
}

// HandleAdminModels GET /admin/api/models
// 返回支持的模型列表及其上游映射
func HandleAdminModels(w http.ResponseWriter, r *http.Request) {
	if !adminAuth(r) {
		adminWriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	models := GetAvailableModels()
	out := make([]map[string]interface{}, 0, len(models))
	for _, m := range models {
		mapping := GetUpstreamConfig(m.ID)
		entry := map[string]interface{}{
			"id":       m.ID,
			"owned_by": m.OwnedBy,
		}
		if mapping != nil {
			entry["upstream_id"] = mapping.UpstreamModelID
			entry["thinking"] = mapping.EnableThinking
			entry["search"] = mapping.AutoWebSearch
			entry["mcp_servers"] = mapping.MCPServers
		}
		out = append(out, entry)
	}

	adminWriteJSON(w, http.StatusOK, map[string]interface{}{
		"models": out,
		"total":  len(out),
	})
}

// HandleAdminTestModel POST /admin/api/test
// body: {"model": "...", "prompt": "..."}
// 直接调用 chat completions 接口做连通性测试
func HandleAdminTestModel(w http.ResponseWriter, r *http.Request) {
	if !adminAuth(r) {
		adminWriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if r.Method != http.MethodPost {
		adminWriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	var body struct {
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		adminWriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if body.Model == "" {
		body.Model = "glm-4-flash"
	}
	if body.Prompt == "" {
		body.Prompt = "用一句话介绍你自己"
	}

	// 内部调用 makeUpstreamRequest
	token, err := getUpstreamToken()
	if err != nil {
		adminWriteJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("get token: %v", err)})
		return
	}

	messages := []Message{{Role: "user", Content: body.Prompt}}
	startedAt := time.Now()
	resp, modelName, err := makeUpstreamRequest(token, messages, body.Model, nil, nil, false, nil)
	if err != nil {
		adminWriteJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("upstream: %v", err)})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		adminWriteJSON(w, http.StatusBadGateway, map[string]interface{}{
			"error":  fmt.Sprintf("upstream status %d", resp.StatusCode),
			"status": resp.StatusCode,
		})
		return
	}

	// 简单读取流式响应，拼接 delta_content
	var content strings.Builder
	scanner := newSSEScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}
		var u UpstreamData
		if json.Unmarshal([]byte(payload), &u) != nil {
			continue
		}
		if u.HasError() {
			adminWriteJSON(w, http.StatusBadGateway, map[string]string{"error": u.GetErrorMessage()})
			return
		}
		if u.Data.DeltaContent != "" {
			content.WriteString(u.Data.DeltaContent)
		}
		if u.Data.Done {
			break
		}
	}

	adminWriteJSON(w, http.StatusOK, map[string]interface{}{
		"ok":         true,
		"model":      modelName,
		"content":    content.String(),
		"elapsed_ms": time.Since(startedAt).Milliseconds(),
	})
}

func maskToken(t string) string {
	if len(t) <= 12 {
		return strings.Repeat("*", len(t))
	}
	return t[:6] + "***" + t[len(t)-4:]
}

// 简单的 SSE scanner（line-based）
type sseScanner struct {
	body io.Reader
	buf  []byte
	line string
}

func newSSEScanner(body io.Reader) *sseScanner {
	return &sseScanner{body: body, buf: make([]byte, 0, 4096)}
}

func (s *sseScanner) Scan() bool {
	tmp := make([]byte, 4096)
	for {
		// 先看 buf 里有没有完整一行
		for i, b := range s.buf {
			if b == '\n' {
				s.line = strings.TrimRight(string(s.buf[:i]), "\r")
				s.buf = s.buf[i+1:]
				return true
			}
		}
		n, err := s.body.Read(tmp)
		if n > 0 {
			s.buf = append(s.buf, tmp[:n]...)
		}
		if err != nil {
			if len(s.buf) > 0 {
				s.line = string(s.buf)
				s.buf = nil
				return true
			}
			return false
		}
	}
}

func (s *sseScanner) Text() string { return s.line }

// ── API Key 管理 ──

// HandleAdminKeysList GET /admin/api/keys
func HandleAdminKeysList(w http.ResponseWriter, r *http.Request) {
	if !adminAuth(r) {
		adminWriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	keys := GetApiKeyManager().List()
	out := make([]map[string]interface{}, 0, len(keys))
	for _, k := range keys {
		entry := map[string]interface{}{
			"key":        k.Key, // 完整 key，方便复制
			"masked":     maskApiKey(k.Key),
			"name":       k.Name,
			"created_at": k.CreatedAt,
			"last_used":  k.LastUsed,
			"use_count":  k.UseCount,
			"enabled":    k.Enabled,
		}
		out = append(out, entry)
	}
	adminWriteJSON(w, http.StatusOK, map[string]interface{}{
		"keys":  out,
		"total": len(out),
	})
}

// HandleAdminKeysCreate POST /admin/api/keys
// body: {"name": "..."}
func HandleAdminKeysCreate(w http.ResponseWriter, r *http.Request) {
	if !adminAuth(r) {
		adminWriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if r.Method != http.MethodPost {
		adminWriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	k, err := GetApiKeyManager().Create(body.Name)
	if err != nil {
		adminWriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	adminWriteJSON(w, http.StatusOK, map[string]interface{}{
		"ok":         true,
		"key":        k.Key, // 注意：完整 key 只在创建时返回一次（前端要提示用户保存）
		"name":       k.Name,
		"created_at": k.CreatedAt,
	})
}

// HandleAdminKeysDelete DELETE /admin/api/keys
// body: {"key": "..."}
func HandleAdminKeysDelete(w http.ResponseWriter, r *http.Request) {
	if !adminAuth(r) {
		adminWriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		adminWriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if err := GetApiKeyManager().Delete(body.Key); err != nil {
		adminWriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	adminWriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// HandleAdminKeysToggle POST /admin/api/keys/toggle
// body: {"key": "...", "enabled": true/false}
func HandleAdminKeysToggle(w http.ResponseWriter, r *http.Request) {
	if !adminAuth(r) {
		adminWriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if r.Method != http.MethodPost {
		adminWriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var body struct {
		Key     string `json:"key"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		adminWriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if err := GetApiKeyManager().SetEnabled(body.Key, body.Enabled); err != nil {
		adminWriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	adminWriteJSON(w, http.StatusOK, map[string]bool{"ok": true, "enabled": body.Enabled})
}

func maskApiKey(k string) string {
	if len(k) <= 12 {
		return strings.Repeat("*", len(k))
	}
	return k[:6] + "..." + k[len(k)-4:]
}
