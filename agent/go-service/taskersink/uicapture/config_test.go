package uicapture

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func timeNow() time.Time { return time.Now() }

// 通配匹配要覆盖三种常见写法，同时不能把无关节点吸进来。
func TestMatchName(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		vertical string
		want     bool
	}{
		{"精确名", []string{"__AutoEcoFarmCollectPanel"}, "__AutoEcoFarmCollectPanel", true},
		{"精确名不匹配", []string{"__AutoEcoFarmCollectPanel"}, "__AutoEcoFarmStoreRanch", false},
		{"全通配", []string{"*"}, "任意节点", true},
		{"前缀通配", []string{"__AutoEcoFarm*"}, "__AutoEcoFarmStoreRanch", true},
		{"前缀通配不中", []string{"__AutoEcoFarm*"}, "AutoStockpileTask", false},
		{"包含通配", []string{"*Panel*"}, "__AutoEcoFarmCollectPanel", true},
		{"包含通配不中", []string{"*Panel*"}, "__AutoEcoFarmStoreRanch", false},
		{"中间通配", []string{"__Auto*Ranch"}, "__AutoEcoFarmStoreRanch", true},
		{"中间通配缺段", []string{"__Auto*Ranch"}, "__AutoEcoFarmCollectPanel", false},
		{"空列表", nil, "__AutoEcoFarmCollectPanel", false},
		{"空串跳过", []string{"", "__AutoEcoFarmCollectPanel"}, "__AutoEcoFarmCollectPanel", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := matchName(c.patterns, c.vertical); got != c.want {
				t.Fatalf("matchName(%v, %q) = %v, want %v", c.patterns, c.vertical, got, c.want)
			}
		})
	}
}

// 默认值要能兜住一份「只写了 enabled 和 nodes」的最小配置。
func TestConfigNormalize(t *testing.T) {
	got := Config{Enabled: true, Nodes: []string{"*"}}.normalize()

	if got.Dir != defaultDir {
		t.Errorf("Dir = %q, want %q", got.Dir, defaultDir)
	}
	if got.Format != "jpg" {
		t.Errorf("Format = %q, want jpg", got.Format)
	}
	if got.Quality != defaultQuality {
		t.Errorf("Quality = %d, want %d", got.Quality, defaultQuality)
	}
	if got.Max != defaultMax {
		t.Errorf("Max = %d, want %d", got.Max, defaultMax)
	}
	if !got.oncePerNode() {
		t.Error("oncePerNode() = false, want true by default")
	}
	if got.cooldown().Milliseconds() != defaultCooldownMS {
		t.Errorf("cooldown = %v, want %dms", got.cooldown(), defaultCooldownMS)
	}
}

// 越界值要被夹回默认，而不是原样透传。
func TestConfigNormalizeClamps(t *testing.T) {
	got := Config{Quality: 200, Max: -3, Format: "webp", Dir: "/shots/"}.normalize()

	if got.Quality != defaultQuality {
		t.Errorf("Quality = %d, want %d", got.Quality, defaultQuality)
	}
	if got.Max != defaultMax {
		t.Errorf("Max = %d, want %d", got.Max, defaultMax)
	}
	if got.Format != "jpg" {
		t.Errorf("Format = %q, want jpg", got.Format)
	}
	if got.Dir != "shots" {
		t.Errorf("Dir = %q, want shots", got.Dir)
	}
}

// 配置文件不存在时要安静地关闭，不能报错。
func TestStoreMissingConfigDisables(t *testing.T) {
	dir := t.TempDir()
	s := newStore()
	s.path = filepath.Join(dir, configFile)

	cfg, patterns, on := s.current(timeNow())
	if on {
		t.Fatal("missing config should be disabled")
	}
	if len(patterns) != 0 {
		t.Fatalf("patterns = %v, want empty", patterns)
	}
	if cfg.Dir != defaultDir {
		t.Fatalf("Dir = %q, want default %q", cfg.Dir, defaultDir)
	}
}

// 改完配置要能热加载，不用重建 sink。
func TestStoreReloadsOnChange(t *testing.T) {
	dir := t.TempDir()
	s := newStore()
	s.path = filepath.Join(dir, configFile)

	write := func(body string) {
		if err := os.WriteFile(s.path, []byte(body), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}

	write(`{"enabled": true, "nodes": ["__AutoEcoFarm*"]}`)
	if _, _, on := s.current(timeNow()); !on {
		t.Fatal("expected enabled after first write")
	}

	write(`{"enabled": false, "nodes": ["*"]}`)
	// 推进时间越过 reloadInterval，触发重新 stat。
	s.lastCheck = timeNow().Add(-2 * reloadInterval)
	if _, _, on := s.current(timeNow()); on {
		t.Fatal("expected disabled after rewrite")
	}
}

// 坏 JSON 要按关闭处理，且不 panic。
func TestStoreInvalidJSONDisables(t *testing.T) {
	dir := t.TempDir()
	s := newStore()
	s.path = filepath.Join(dir, configFile)
	if err := os.WriteFile(s.path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, _, on := s.current(timeNow()); on {
		t.Fatal("invalid json should be disabled")
	}
}

// 同一任务内每个节点只存一张；换任务后重新计数。
func TestAllowOncePerNode(t *testing.T) {
	s := NewSink()
	cfg := Config{Max: 2}.normalize()
	now := timeNow()

	if !s.allow(1, "A", now, cfg) {
		t.Fatal("first hit should be allowed")
	}
	if s.allow(1, "A", now, cfg) {
		t.Fatal("same node in same task should be deduplicated")
	}
	if !s.allow(1, "B", now, cfg) {
		t.Fatal("second distinct node should be allowed")
	}
	// 上限 2 张已用完。
	if s.allow(2, "A", now, cfg) {
		t.Fatal("process-wide cap should stop further captures")
	}
}

// 关闭 OncePerNode 后按冷却放行。
func TestAllowCooldown(t *testing.T) {
	s := NewSink()
	off := false
	cfg := Config{Max: 10, OncePerNode: &off, CooldownMS: 3000}.normalize()
	now := timeNow()

	if !s.allow(1, "A", now, cfg) {
		t.Fatal("first hit should be allowed")
	}
	if s.allow(1, "A", now.Add(time.Second), cfg) {
		t.Fatal("within cooldown should be skipped")
	}
	if !s.allow(1, "A", now.Add(4*time.Second), cfg) {
		t.Fatal("after cooldown should be allowed again")
	}
}

// 文件名要能安全落盘，同时保留可读性。
func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"__AutoEcoFarmCollectPanel": "__AutoEcoFarmCollectPanel",
		"a/b:c*d":                   "a_b_c_d",
		"line\nbreak":               "line_break",
	}
	for in, want := range cases {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}

	long := make([]byte, 200)
	for i := range long {
		long[i] = 'x'
	}
	if got := safeName(string(long)); len(got) != 80 {
		t.Errorf("safeName(long) len = %d, want 80", len(got))
	}
}

// event 只认 pipeline，其余一律回落到 recognition。
func TestConfigEvent(t *testing.T) {
	cases := map[string]string{
		"":            "recognition",
		"recognition": "recognition",
		"pipeline":    "pipeline",
		"PIPELINE":    "pipeline",
		"weird":       "recognition",
	}
	for in, want := range cases {
		if got := (Config{Event: in}).normalize().Event; got != want {
			t.Errorf("Event %q -> %q, want %q", in, got, want)
		}
	}
}
