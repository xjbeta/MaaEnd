// Package uicapture 按配置文件把指定界面的截图留证到用户数据目录。
//
// 与 diagcapture 的区别：diagcapture 按「模板匹配贴近判定线」被动留证，
// 本包按「节点名命中」主动留证 —— 给每个想观察的界面存一张原图，
// 用来量 ROI、写新识别，或排查「当时屏幕到底是什么样」。
//
// 设计上刻意做成零编译配置：
//   - 开关与目标节点都写在 files/debug/ui_capture.json，ADB 推一份即可；
//   - 每 2 秒查一次文件 mtime，改完不用重启任务；
//   - 取图走 ctrl.CacheImageInto，复用框架已缓存的帧，不额外截图；
//   - 任何异常都只打日志并按「关闭」处理，不影响任务本身。
package uicapture

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/MaaXYZ/MaaEnd/agent/go-service/pkg/fsutil"
	"github.com/rs/zerolog/log"
)

const (
	// configFile 相对用户数据目录，即 files/debug/ui_capture.json。
	configFile = "ui_capture.json"

	// reloadInterval 配置热加载间隔。改配置后最多这么久生效。
	reloadInterval = 2 * time.Second

	defaultDir        = "ui_capture"
	defaultFormat     = "jpg"
	defaultQuality    = 85
	defaultMax        = 60
	defaultCooldownMS = 3000
)

// Config 是 ui_capture.json 的映射。所有字段都可省略，省略后取默认值。
type Config struct {
	// Enabled 总开关。文件不存在时视为关闭。
	Enabled bool `json:"enabled"`
	// Nodes 目标节点名，支持 * 通配（如 "__AutoEcoFarm*"、"*Panel*"、"*"）。
	Nodes []string `json:"nodes"`
	// Dir 输出目录，相对用户数据目录的 debug/。
	Dir string `json:"dir"`
	// Format 输出格式：jpg（默认）或 png。
	Format string `json:"format"`
	// Quality JPEG 质量，默认 85。
	Quality int `json:"quality"`
	// Max 单次进程最多保存的张数，默认 60，用于保护磁盘。
	Max int `json:"max"`
	// OncePerNode 每个任务内同一节点只存一张，默认 true。
	OncePerNode *bool `json:"once_per_node"`
	// CooldownMS 未开启 OncePerNode 时的单节点冷却，默认 3000 毫秒。
	CooldownMS int `json:"cooldown_ms"`
}

// normalize 补齐默认值并裁剪越界值。
func (c Config) normalize() Config {
	if c.Dir == "" {
		c.Dir = defaultDir
	}
	c.Dir = strings.Trim(c.Dir, "/")

	switch strings.ToLower(c.Format) {
	case "png":
		c.Format = "png"
	default:
		c.Format = defaultFormat
	}

	if c.Quality <= 0 || c.Quality > 100 {
		c.Quality = defaultQuality
	}
	if c.Max <= 0 {
		c.Max = defaultMax
	}
	if c.CooldownMS <= 0 {
		c.CooldownMS = defaultCooldownMS
	}
	return c
}

// oncePerNode 默认 true。
func (c Config) oncePerNode() bool {
	return c.OncePerNode == nil || *c.OncePerNode
}

func (c Config) cooldown() time.Duration {
	return time.Duration(c.CooldownMS) * time.Millisecond
}

// store 管理配置文件的读取与热加载。
type store struct {
	mu sync.Mutex

	path string
	cfg  Config

	lastCheck time.Time
	modTime   time.Time
	size      int64
	loaded    bool

	// warnOnce 保证同一次加载失败只刷一条日志，避免每帧刷屏。
	warnOnce bool
}

func newStore() *store {
	return &store{path: fsutil.UserDataPath("debug", configFile)}
}

// current 返回当前生效的配置与编译好的匹配串，第三个返回值表示是否开启。
//
// 调用频率很高（每次识别成功一次），所以只在 reloadInterval 之后才去 stat，
// 其余时候直接返回缓存。
func (s *store) current(now time.Time) (Config, []string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.loaded || now.Sub(s.lastCheck) >= reloadInterval {
		s.lastCheck = now
		s.reloadLocked()
	}

	if !s.cfg.Enabled || len(s.cfg.Nodes) == 0 {
		return s.cfg, nil, false
	}
	return s.cfg, s.cfg.Nodes, true
}

// reloadLocked 在 mtime/size 变化时重新解析配置；调用方需持有锁。
func (s *store) reloadLocked() {
	info, err := os.Stat(s.path)
	if err != nil {
		// 文件不存在是正常状态（默认关闭），不重复告警。
		s.cfg = Config{}.normalize()
		s.loaded = true
		s.modTime = time.Time{}
		s.size = 0
		return
	}
	if s.loaded && info.ModTime().Equal(s.modTime) && info.Size() == s.size {
		return
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		s.disableLocked("read failed", err)
		return
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		s.disableLocked("invalid json", err)
		return
	}

	s.cfg = cfg.normalize()
	s.modTime = info.ModTime()
	s.size = info.Size()
	s.loaded = true
	s.warnOnce = false

	log.Info().
		Str("component", component).
		Bool("enabled", s.cfg.Enabled).
		Int("nodes", len(s.cfg.Nodes)).
		Str("path", s.path).
		Msg("ui capture config loaded")
}

// disableLocked 解析失败时按关闭处理，并且同一份坏文件只告警一次。
func (s *store) disableLocked(reason string, err error) {
	s.cfg = Config{}.normalize()
	s.loaded = true
	if s.warnOnce {
		return
	}
	s.warnOnce = true
	log.Warn().
		Err(err).
		Str("component", component).
		Str("reason", reason).
		Str("path", s.path).
		Msg("ui capture disabled, fix the config file and it reloads automatically")
}

// matchName 判断节点名是否命中目标列表，支持 * 通配。
func matchName(patterns []string, name string) bool {
	for _, p := range patterns {
		if p == "" {
			continue
		}
		if p == "*" || p == name {
			return true
		}
		if strings.Contains(p, "*") && wildcardMatch(p, name) {
			return true
		}
	}
	return false
}

// wildcardMatch 逐段匹配 * 通配：前缀、后缀、中间各段按顺序出现即可。
func wildcardMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}

	prefix := parts[0]
	if !strings.HasPrefix(s, prefix) {
		return false
	}
	s = s[len(prefix):]

	last := parts[len(parts)-1]
	if last != "" {
		if !strings.HasSuffix(s, last) {
			return false
		}
		s = s[:len(s)-len(last)]
	}

	for _, mid := range parts[1 : len(parts)-1] {
		if mid == "" {
			continue
		}
		i := strings.Index(s, mid)
		if i < 0 {
			return false
		}
		s = s[i+len(mid):]
	}
	return true
}
