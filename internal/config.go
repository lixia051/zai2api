package internal

import (
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	// Server
	Port string

	// API Configuration
	APIEndpoint         string
	BrowserBridgeURL    string
	BrowserBridgeSecret string
	AuthTokens          []string // 支持多个 API Key（逗号分隔）
	BackupTokens        []string // 支持多个 Backup Token（用于多模态，逗号分隔）

	// Captcha Provider
	CaptchaProviderURL string // e.g. http://127.0.0.1:9876

	// Feature Configuration
	DebugLogging            bool
	ToolSupport             bool
	ForceToolChoiceRequired bool
	RetryCount              int
	SkipAuthToken           bool
	ScanLimit               int
	LogLevel                string

	// 匿名 token 池（无 TokenManager / BACKUP_TOKEN 时启用；已配置上游 token 时不使用池）
	AnonymousPoolSize               int
	AnonymousTokenTTLSeconds        int
	AnonymousRefreshIntervalSeconds int
	AnonymousFetchMaxRetries        int

	// Display
	Note []string // 多行备注，在 / 显示
}

var Cfg *Config

func getEnvString(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	return val == "true" || val == "1" || val == "yes"
}

func getEnvInt(key string, defaultVal int) int {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	if i, err := strconv.Atoi(val); err == nil {
		return i
	}
	return defaultVal
}

// getEnvStringSlice 解析逗号分隔的字符串为切片
func getEnvStringSlice(key string) []string {
	val := os.Getenv(key)
	if val == "" {
		return nil
	}
	parts := strings.Split(val, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// parseNoteLines 解析多行备注，支持 \n 换行和 | 分隔
func parseNoteLines(note string) []string {
	if note == "" {
		return nil
	}
	// 支持 \n 和 | 作为换行符
	note = strings.ReplaceAll(note, "\\n", "\n")
	note = strings.ReplaceAll(note, "|", "\n")
	lines := strings.Split(note, "\n")
	var result []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func LoadConfig() {
	godotenv.Load()

	Cfg = &Config{
		// Server
		Port: getEnvString("PORT", "8000"),

		// API Configuration
		APIEndpoint:         getEnvString("API_ENDPOINT", "https://chat.z.ai/api/v2/chat/completions"),
		BrowserBridgeURL:    getEnvString("BROWSER_BRIDGE_URL", ""),
		BrowserBridgeSecret: getEnvString("BROWSER_BRIDGE_SECRET", ""),
		AuthTokens:          getEnvStringSlice("AUTH_TOKEN"),
		BackupTokens:        getEnvStringSlice("BACKUP_TOKEN"),
		CaptchaProviderURL:  getEnvString("CAPTCHA_PROVIDER_URL", ""),

		// Feature Configuration
		DebugLogging:            getEnvBool("DEBUG_LOGGING", false),
		ToolSupport:             getEnvBool("TOOL_SUPPORT", true),
		ForceToolChoiceRequired: getEnvBool("FORCE_TOOL_CHOICE_REQUIRED", false),
		RetryCount:              getEnvInt("RETRY_COUNT", 5),
		SkipAuthToken:           getEnvBool("SKIP_AUTH_TOKEN", false),
		ScanLimit:               getEnvInt("SCAN_LIMIT", 200000),
		LogLevel:                getEnvString("LOG_LEVEL", "info"),

		AnonymousPoolSize:               getEnvInt("ANONYMOUS_POOL_SIZE", 4),
		AnonymousTokenTTLSeconds:        getEnvInt("ANONYMOUS_TOKEN_TTL_SECONDS", 1200),
		AnonymousRefreshIntervalSeconds: getEnvInt("ANONYMOUS_REFRESH_INTERVAL_SECONDS", 90),
		AnonymousFetchMaxRetries:        getEnvInt("ANONYMOUS_FETCH_MAX_RETRIES", 3),

		// Display
		Note: parseNoteLines(getEnvString("NOTE", "")),
	}
}

func ValidateAuthToken(token string) bool {
	if Cfg.SkipAuthToken {
		return true
	}
	if len(Cfg.AuthTokens) == 0 && len(GetApiKeyManager().List()) == 0 {
		LogWarn("既未配置 AUTH_TOKEN 也没有用户创建的 API Key，拒绝所有请求")
		return false
	}
	return ValidateAnyApiKey(token)
}

var backupTokenIndex int

func GetBackupToken() string {
	if len(Cfg.BackupTokens) == 0 {
		return ""
	}
	token := Cfg.BackupTokens[backupTokenIndex%len(Cfg.BackupTokens)]
	backupTokenIndex++
	return token
}
