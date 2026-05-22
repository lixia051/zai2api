package internal

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ApiKey 单个 API Key 的元信息
type ApiKey struct {
	Key       string `json:"key"`        // 完整 key（sk-xxx）
	Name      string `json:"name"`       // 用户给的显示名
	CreatedAt int64  `json:"created_at"` // unix 秒
	LastUsed  int64  `json:"last_used"`  // unix 秒，0 表示从未使用
	UseCount  int64  `json:"use_count"`  // 累计使用次数
	Enabled   bool   `json:"enabled"`    // 是否启用
}

// ApiKeyManager 管理用户创建的 API Key
type ApiKeyManager struct {
	mu       sync.RWMutex
	keys     map[string]*ApiKey // key -> ApiKey
	dataDir  string
	filename string
}

var (
	apiKeyManager *ApiKeyManager
	apiKeyOnce    sync.Once
)

// GetApiKeyManager 单例
func GetApiKeyManager() *ApiKeyManager {
	apiKeyOnce.Do(func() {
		apiKeyManager = &ApiKeyManager{
			keys:     make(map[string]*ApiKey),
			dataDir:  "data",
			filename: "api_keys.json",
		}
		if err := apiKeyManager.load(); err != nil {
			LogWarn("ApiKeyManager 加载失败: %v", err)
		}
	})
	return apiKeyManager
}

func (m *ApiKeyManager) path() string {
	return filepath.Join(m.dataDir, m.filename)
}

// load 从磁盘读
func (m *ApiKeyManager) load() error {
	if err := os.MkdirAll(m.dataDir, 0755); err != nil {
		return err
	}
	data, err := os.ReadFile(m.path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var arr []*ApiKey
	if err := json.Unmarshal(data, &arr); err != nil {
		return fmt.Errorf("parse %s: %w", m.path(), err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range arr {
		if k.Key != "" {
			m.keys[k.Key] = k
		}
	}
	LogInfo("ApiKeyManager 已加载 %d 个 API Key", len(m.keys))
	return nil
}

// save 写到磁盘（原子）
func (m *ApiKeyManager) save() error {
	m.mu.RLock()
	arr := make([]*ApiKey, 0, len(m.keys))
	for _, k := range m.keys {
		copy := *k
		arr = append(arr, &copy)
	}
	m.mu.RUnlock()
	sort.Slice(arr, func(i, j int) bool { return arr[i].CreatedAt < arr[j].CreatedAt })

	data, err := json.MarshalIndent(arr, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.dataDir, 0755); err != nil {
		return err
	}
	tmpFile := m.path() + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpFile, m.path())
}

// generateKey 生成形如 sk-xxxxxxxx (32 hex chars 后缀) 的 key
func generateApiKey() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		// 极端 fallback
		return fmt.Sprintf("sk-%d-%d", time.Now().UnixNano(), atomic.AddInt64(&fallbackKeyCounter, 1))
	}
	return "sk-" + hex.EncodeToString(b)
}

var fallbackKeyCounter int64

// Create 创建新 key
func (m *ApiKeyManager) Create(name string) (*ApiKey, error) {
	if name == "" {
		name = "未命名"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	k := &ApiKey{
		Key:       generateApiKey(),
		Name:      name,
		CreatedAt: time.Now().Unix(),
		Enabled:   true,
	}
	m.mu.Lock()
	m.keys[k.Key] = k
	m.mu.Unlock()
	if err := m.save(); err != nil {
		// 回滚
		m.mu.Lock()
		delete(m.keys, k.Key)
		m.mu.Unlock()
		return nil, fmt.Errorf("写文件失败: %w", err)
	}
	return k, nil
}

// Delete 删除
func (m *ApiKeyManager) Delete(key string) error {
	m.mu.Lock()
	if _, ok := m.keys[key]; !ok {
		m.mu.Unlock()
		return fmt.Errorf("key 不存在")
	}
	delete(m.keys, key)
	m.mu.Unlock()
	return m.save()
}

// SetEnabled 启用/禁用
func (m *ApiKeyManager) SetEnabled(key string, enabled bool) error {
	m.mu.Lock()
	k, ok := m.keys[key]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("key 不存在")
	}
	k.Enabled = enabled
	m.mu.Unlock()
	return m.save()
}

// Validate 验证一个 key 是否有效，并增加使用计数
func (m *ApiKeyManager) Validate(key string) bool {
	m.mu.RLock()
	k, ok := m.keys[key]
	m.mu.RUnlock()
	if !ok || !k.Enabled {
		return false
	}
	atomic.AddInt64(&k.UseCount, 1)
	atomic.StoreInt64(&k.LastUsed, time.Now().Unix())
	// 计数变化暂不立即落盘（避免每个请求都写磁盘），由 Save 周期统一持久化或下次 CRUD 时一并落盘
	return true
}

// List 返回当前所有 key 的快照
func (m *ApiKeyManager) List() []*ApiKey {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*ApiKey, 0, len(m.keys))
	for _, k := range m.keys {
		copy := *k
		out = append(out, &copy)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// PersistCounters 周期性把使用计数落盘
func (m *ApiKeyManager) PersistCounters() {
	if err := m.save(); err != nil {
		LogWarn("ApiKeyManager 持久化计数失败: %v", err)
	}
}

// StartPeriodicSave 启动后台周期保存（用于持久化 use_count/last_used）
func (m *ApiKeyManager) StartPeriodicSave() {
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			m.PersistCounters()
		}
	}()
}

// ── Auth integration ──

// ValidateAnyApiKey 入口：先查环境变量 AuthTokens，再查 ApiKeyManager
func ValidateAnyApiKey(key string) bool {
	if Cfg.SkipAuthToken {
		return true
	}
	// env-based AUTH_TOKEN
	for _, t := range Cfg.AuthTokens {
		if subtle.ConstantTimeCompare([]byte(t), []byte(key)) == 1 {
			return true
		}
	}
	// dynamic ApiKeyManager
	return GetApiKeyManager().Validate(key)
}
