package pluginfeed

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/dezhishen/upkit/internal/fsutil"
	"github.com/dezhishen/upkit/internal/util"
)

// FileName 是订阅与授权记录的文件名（位于设置目录下）。
const FileName = "subscriptions.yaml"

// Authorization 是订阅功能与域名的授权记录。
//
// 这个文件里的每一项都只能**由界面显式确认**后写入：手工编辑它等价于跳过授权
// 确认，因此它由程序独占读写，不作为用户的配置入口。
type Authorization struct {
	// FeatureAuthorized 是订阅功能总开关，默认 false；用户首次启用时置为 true。
	FeatureAuthorized   bool      `yaml:"feature_authorized"`
	FeatureAuthorizedAt time.Time `yaml:"feature_authorized_at,omitempty"`
	// Subscriptions 是已添加的订阅（添加时即授权其域名）。
	Subscriptions []Subscription `yaml:"subscriptions,omitempty"`
	// PackageHosts 是跨域包下载已授权的域名（按域名记住）。
	PackageHosts []HostAuthorization `yaml:"package_hosts,omitempty"`
}

// Subscription 是一条订阅记录。
type Subscription struct {
	URL string `yaml:"url"`
	// Host 是添加订阅时用户授权信任的域名。
	Host string `yaml:"host"`
	// Enabled 缺省启用。
	Enabled *bool     `yaml:"enabled,omitempty"`
	AddedAt time.Time `yaml:"added_at"`
	// TTLSeconds 是订阅缓存的保鲜期（缺省 1 小时）。
	TTLSeconds int `yaml:"ttl_seconds,omitempty"`
	// LastFetched / LastError 记录最近一次拉取结果，供界面展示。
	LastFetched time.Time `yaml:"last_fetched,omitempty"`
	LastError   string    `yaml:"last_error,omitempty"`
}

// HostAuthorization 是一条域名授权记录。
type HostAuthorization struct {
	Host         string    `yaml:"host"`
	AuthorizedAt time.Time `yaml:"authorized_at"`
	// ForSubscription 记录这次授权由哪个订阅触发，便于撤销时判断影响面。
	ForSubscription string `yaml:"for_subscription,omitempty"`
}

// EnabledValue 报告订阅是否启用（缺省启用）。
func (s Subscription) EnabledValue() bool {
	if s.Enabled == nil {
		return true
	}
	return *s.Enabled
}

// TTL 返回缓存保鲜期。
func (s Subscription) TTL() time.Duration {
	if s.TTLSeconds <= 0 {
		return time.Hour
	}
	return time.Duration(s.TTLSeconds) * time.Second
}

// Store 是授权记录的读写入口。
type Store struct {
	path string

	// mu 保护 auth 与写盘。
	//
	// Store 会被 UI 线程（增删订阅、改启停）与 tea.Cmd 的 goroutine
	// （安装时查授权）同时访问：没有锁时，一边 append/替换 Subscriptions
	// 切片、另一边读同一片内存，是真实的 data race（-race 可复现），
	// 并发 Save 还会丢更新。
	mu   sync.RWMutex
	auth Authorization
}

// LoadStore 读取授权记录；文件不存在时返回一份未授权的空记录。
func LoadStore(path string) (*Store, error) {
	p := util.ExpandPath(strings.TrimSpace(path))
	if p == "" {
		return nil, fmt.Errorf("授权记录的路径为空")
	}
	s := &Store{path: p}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("读取订阅记录 %s: %w", p, err)
	}
	if err := yaml.Unmarshal(data, &s.auth); err != nil {
		return nil, fmt.Errorf("解析订阅记录 %s: %w", p, err)
	}
	return s, nil
}

// Path 返回记录文件的实际路径。
func (s *Store) Path() string { return s.path }

// Authorization 返回记录的快照。
func (s *Store) Authorization() Authorization { return s.auth }

// Save 原子写回记录（自带加锁，供外部调用）。
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// saveLocked 与 Save 相同，但要求调用方已持写锁。
func (s *Store) saveLocked() error {
	data, err := yaml.Marshal(&s.auth)
	if err != nil {
		return fmt.Errorf("序列化订阅记录: %w", err)
	}
	return fsutil.WriteFileAtomic(s.path, data, 0o600)
}

// FeatureAuthorized 报告订阅功能是否已被授权启用。
func (s *Store) FeatureAuthorized() bool { return s.auth.FeatureAuthorized }

// AuthorizeFeature 记录「用户已同意启用订阅功能」。
//
// 必须由界面在用户确认后调用：订阅会把网络上的二进制装到本机并执行。
func (s *Store) AuthorizeFeature() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auth.FeatureAuthorized {
		return nil
	}
	s.auth.FeatureAuthorized = true
	s.auth.FeatureAuthorizedAt = time.Now()
	return s.saveLocked()
}

// RevokeFeature 关闭订阅功能（保留已添加的订阅记录，只是不再拉取）。
func (s *Store) RevokeFeature() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auth.FeatureAuthorized {
		return nil
	}
	s.auth.FeatureAuthorized = false
	return s.saveLocked()
}

// Subscriptions 返回全部订阅（复制一份，避免调用方改动内部状态）。
func (s *Store) Subscriptions() []Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Subscription, len(s.auth.Subscriptions))
	copy(out, s.auth.Subscriptions)
	return out
}

// EnabledSubscriptions 返回已启用的订阅。
func (s *Store) EnabledSubscriptions() []Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Subscription, 0, len(s.auth.Subscriptions))
	for _, sub := range s.auth.Subscriptions {
		if sub.EnabledValue() {
			out = append(out, sub)
		}
	}
	return out
}

// Subscription 按 URL 查找订阅。
func (s *Store) Subscription(rawURL string) (Subscription, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.subscriptionLocked(rawURL)
}

// subscriptionLocked 与 Subscription 相同，但要求调用方已持锁。
func (s *Store) subscriptionLocked(rawURL string) (Subscription, bool) {
	for _, sub := range s.auth.Subscriptions {
		if sub.URL == rawURL {
			return sub, true
		}
	}
	return Subscription{}, false
}

// AddSubscription 添加订阅并授权其域名。
//
// 调用它即代表**用户在界面上确认过**「信任来自该域名的插件下载」；这里再校验一次
// 地址合法性并记录域名，供后续审计与撤销。
//
// 功能开关在这里再强制一次：默认关闭，仅靠界面不点按钮是不够的。
func (s *Store) AddSubscription(rawURL string) (Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auth.FeatureAuthorized {
		return Subscription{}, fmt.Errorf("订阅功能尚未启用：请先在界面上确认启用（默认关闭）")
	}
	host, err := FeedHost(rawURL)
	if err != nil {
		return Subscription{}, err
	}
	if _, exists := s.subscriptionLocked(rawURL); exists {
		return Subscription{}, fmt.Errorf("订阅已存在: %s", rawURL)
	}
	sub := Subscription{
		URL:     strings.TrimSpace(rawURL),
		Host:    host,
		AddedAt: time.Now(),
	}
	s.auth.Subscriptions = append(s.auth.Subscriptions, sub)
	if err := s.saveLocked(); err != nil {
		return Subscription{}, err
	}
	return sub, nil
}

// RemoveSubscription 移除订阅。
//
// 只删除订阅本身：为跨域下载授权过的域名保持授权（可能被其它订阅复用），
// 需要收紧时在界面上单独撤销域名授权。
func (s *Store) RemoveSubscription(rawURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := make([]Subscription, 0, len(s.auth.Subscriptions))
	found := false
	for _, sub := range s.auth.Subscriptions {
		if sub.URL == rawURL {
			found = true
			continue
		}
		kept = append(kept, sub)
	}
	if !found {
		return fmt.Errorf("订阅不存在: %s", rawURL)
	}
	s.auth.Subscriptions = kept
	return s.saveLocked()
}

// SetSubscriptionEnabled 启用或停用一条订阅。
func (s *Store) SetSubscriptionEnabled(rawURL string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.auth.Subscriptions {
		if s.auth.Subscriptions[i].URL != rawURL {
			continue
		}
		v := enabled
		s.auth.Subscriptions[i].Enabled = &v
		return s.saveLocked()
	}
	return fmt.Errorf("订阅不存在: %s", rawURL)
}

// MarkFetched 记录一次拉取的结果。
func (s *Store) MarkFetched(rawURL string, fetchErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.auth.Subscriptions {
		if s.auth.Subscriptions[i].URL != rawURL {
			continue
		}
		s.auth.Subscriptions[i].LastFetched = time.Now()
		if fetchErr == nil {
			s.auth.Subscriptions[i].LastError = ""
		} else {
			s.auth.Subscriptions[i].LastError = fetchErr.Error()
		}
		return s.saveLocked()
	}
	return fmt.Errorf("订阅不存在: %s", rawURL)
}

// HostAuthorized 报告某个域名是否已授权用于跨域包下载。
func (s *Store) HostAuthorized(host string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hostAuthorizedLocked(host)
}

// hostAuthorizedLocked 与 HostAuthorized 相同，但要求调用方已持锁。
func (s *Store) hostAuthorizedLocked(host string) bool {
	host = normalizeHost(host)
	for _, h := range s.auth.PackageHosts {
		if h.Host == host {
			return true
		}
	}
	return false
}

// AuthorizeHost 记录「用户已同意从该域名下载插件包」。
//
// 必须由界面在用户确认后调用：非相对路径的包地址意味着订阅可以把下载指到别处。
func (s *Store) AuthorizeHost(host, forSubscription string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auth.FeatureAuthorized {
		return fmt.Errorf("订阅功能尚未启用：请先在界面上确认启用（默认关闭）")
	}
	host = normalizeHost(host)
	if host == "" {
		return fmt.Errorf("域名为空")
	}
	if s.hostAuthorizedLocked(host) {
		return nil
	}
	s.auth.PackageHosts = append(s.auth.PackageHosts, HostAuthorization{
		Host:            host,
		AuthorizedAt:    time.Now(),
		ForSubscription: forSubscription,
	})
	return s.saveLocked()
}

// RevokeHost 撤销某个域名的下载授权。
func (s *Store) RevokeHost(host string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	host = normalizeHost(host)
	kept := make([]HostAuthorization, 0, len(s.auth.PackageHosts))
	found := false
	for _, h := range s.auth.PackageHosts {
		if h.Host == host {
			found = true
			continue
		}
		kept = append(kept, h)
	}
	if !found {
		return fmt.Errorf("域名未授权: %s", host)
	}
	s.auth.PackageHosts = kept
	return s.saveLocked()
}

// PackageHosts 返回全部已授权的下载域名。
func (s *Store) PackageHosts() []HostAuthorization {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]HostAuthorization, len(s.auth.PackageHosts))
	copy(out, s.auth.PackageHosts)
	return out
}

func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}
