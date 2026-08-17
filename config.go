package main

// config.go owns the plugin configuration file. It stores configuration only
// (templates, selected template, placeholders, write concurrency); runtime
// state such as execution progress lives in memory and is never persisted.
import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// placeholderNamePattern constrains placeholder names to word characters so
// they stay usable inside {name} template tokens.
var placeholderNamePattern = regexp.MustCompile(`^\w+$`)

const (
	defaultWriteConcurrency = 8
	minWriteConcurrency     = 1
	maxWriteConcurrency     = 64
)

type Template struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Placeholder struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type Config struct {
	Templates        []Template    `json:"templates"`
	SelectedTemplate string        `json:"selected_template"`
	Placeholders     []Placeholder `json:"placeholders"`
	WriteConcurrency int           `json:"write_concurrency"`
}

// ConfigStore guards the on-disk config file and hands out snapshots.
type ConfigStore struct {
	mu   sync.Mutex
	path string
	cfg  *Config
}

func dataDir() string {
	if dir := strings.TrimSpace(os.Getenv("CPA_PROXY_MANAGER_DATA_DIR")); dir != "" {
		return dir
	}
	return filepath.Join("data", "cpa-proxy-manager")
}

func defaultConfig() *Config {
	return &Config{
		Templates: []Template{
			{ID: "t1", Name: "Resin 默认池", URL: "http://Default.{account}:token@resin-host:2260"},
		},
		SelectedTemplate: "t1",
		Placeholders:     []Placeholder{},
		WriteConcurrency: defaultWriteConcurrency,
	}
}

// normalize fills omitted defaults so partially-saved files keep working.
func (c *Config) normalize() {
	if c.WriteConcurrency == 0 {
		c.WriteConcurrency = defaultWriteConcurrency
	}
	if c.Templates == nil {
		c.Templates = []Template{}
	}
	if c.Placeholders == nil {
		c.Placeholders = []Placeholder{}
	}
}

func (c *Config) Validate() error {
	if len(c.Templates) == 0 {
		return errors.New("至少保留一个模板")
	}
	names := make(map[string]struct{}, len(c.Templates))
	ids := make(map[string]struct{}, len(c.Templates))
	for _, tpl := range c.Templates {
		name := strings.TrimSpace(tpl.Name)
		if name == "" {
			return errors.New("模板名称不能为空")
		}
		if _, dup := names[name]; dup {
			return fmt.Errorf("模板名称重复：%s", name)
		}
		names[name] = struct{}{}
		id := strings.TrimSpace(tpl.ID)
		if id == "" {
			return errors.New("模板 ID 不能为空")
		}
		if _, dup := ids[id]; dup {
			return fmt.Errorf("模板 ID 重复：%s", id)
		}
		ids[id] = struct{}{}
		urlValue := strings.TrimSpace(tpl.URL)
		if urlValue != "" && !ValidateTemplateURL(urlValue) {
			return fmt.Errorf("模板「%s」的地址不是合法的代理 URL（支持 http、https、socks5、socks5h）", name)
		}
	}
	hasSelected := false
	for _, tpl := range c.Templates {
		if tpl.ID == c.SelectedTemplate {
			hasSelected = true
			break
		}
	}
	if !hasSelected {
		return errors.New("选中的模板不存在")
	}
	phNames := make(map[string]struct{}, len(c.Placeholders))
	for _, ph := range c.Placeholders {
		name := strings.TrimSpace(ph.Name)
		if name == "" {
			return errors.New("占位符名称不能为空")
		}
		if _, dup := phNames[name]; dup {
			return fmt.Errorf("占位符名称重复：%s", name)
		}
		if !placeholderNamePattern.MatchString(name) {
			return fmt.Errorf("占位符名称仅限字母、数字、下划线：%s", name)
		}
		phNames[name] = struct{}{}
		if strings.TrimSpace(ph.Path) == "" {
			return fmt.Errorf("占位符「%s」的 JSONPath 不能为空", name)
		}
	}
	if c.WriteConcurrency < minWriteConcurrency || c.WriteConcurrency > maxWriteConcurrency {
		return fmt.Errorf("写入并发数需在 %d 到 %d 之间", minWriteConcurrency, maxWriteConcurrency)
	}
	return nil
}

// Selected returns the currently selected template.
func (c *Config) Selected() (Template, bool) {
	for _, tpl := range c.Templates {
		if tpl.ID == c.SelectedTemplate {
			return tpl, true
		}
	}
	return Template{}, false
}

func LoadConfig() (*ConfigStore, error) {
	store := &ConfigStore{path: filepath.Join(dataDir(), "config.json")}
	raw, errRead := os.ReadFile(store.path)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			cfg := defaultConfig()
			store.cfg = cfg
			return store, nil
		}
		return nil, fmt.Errorf("读取配置失败：%w", errRead)
	}
	cfg := &Config{}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if errUnmarshal := json.Unmarshal(raw, cfg); errUnmarshal != nil {
			return nil, fmt.Errorf("解析配置失败：%w", errUnmarshal)
		}
	}
	cfg.normalize()
	store.cfg = cfg
	return store, nil
}

var (
	configStoreOnce sync.Once
	configStoreInst *ConfigStore
)

// configStore returns the process-wide config store. When the on-disk file is
// unreadable the plugin still serves defaults so the page keeps working.
func configStore() *ConfigStore {
	configStoreOnce.Do(func() {
		store, err := LoadConfig()
		if err != nil || store == nil {
			store = &ConfigStore{path: filepath.Join(dataDir(), "config.json"), cfg: defaultConfig()}
		}
		configStoreInst = store
	})
	return configStoreInst
}

// Snapshot returns a deep copy safe for concurrent readers.
func (s *ConfigStore) Snapshot() *Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.clone()
}

// Save validates and persists a new configuration atomically.
func (s *ConfigStore) Save(cfg *Config) error {
	if cfg == nil {
		return errors.New("配置不能为空")
	}
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return err
	}
	raw, errMarshal := json.MarshalIndent(cfg, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("序列化配置失败：%w", errMarshal)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if errMkdir := os.MkdirAll(filepath.Dir(s.path), 0o755); errMkdir != nil {
		return fmt.Errorf("创建配置目录失败：%w", errMkdir)
	}
	tmp := s.path + ".tmp"
	if errWrite := os.WriteFile(tmp, raw, 0o600); errWrite != nil {
		return fmt.Errorf("写入配置失败：%w", errWrite)
	}
	if errRename := os.Rename(tmp, s.path); errRename != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("保存配置失败：%w", errRename)
	}
	s.cfg = cfg.clone()
	return nil
}

func (c *Config) clone() *Config {
	if c == nil {
		return nil
	}
	dup := *c
	dup.Templates = append([]Template(nil), c.Templates...)
	dup.Placeholders = append([]Placeholder(nil), c.Placeholders...)
	return &dup
}
