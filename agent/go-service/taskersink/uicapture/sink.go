package uicapture

import (
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MaaXYZ/MaaEnd/agent/go-service/pkg/fsutil"
	maa "github.com/MaaXYZ/maa-framework-go/v4"
	"github.com/rs/zerolog/log"
)

const component = "uicapture"

var _ maa.ContextEventSink = (*Sink)(nil)

// Sink 监听节点识别成功事件，把配置命中界面的当前画面存成图片。
type Sink struct {
	store *store

	mu sync.Mutex

	// taskID 当前任务 ID；任务切换时重置去重状态，让每次运行都能各存一张。
	taskID uint64
	// saved 每个节点在本任务内已保存的张数。
	saved map[string]int
	// lastAt 每个节点上次保存时间，用于未开启 OncePerNode 时的冷却。
	lastAt map[string]time.Time
	// total 本进程已保存张数，用于保护磁盘。
	total  int
	capped bool
}

// NewSink 创建界面留证 sink。
func NewSink() *Sink {
	return &Sink{
		store:  newStore(),
		saved:  make(map[string]int),
		lastAt: make(map[string]time.Time),
	}
}

// 其余 Context 事件与本功能无关，空实现即可。

// OnNodePipelineNode 处理节点完成事件（配置 event=pipeline 时用它）。
//
// 与识别事件的区别：这一支在动作与 post_wait_freezes 都结束之后触发，
// 拿到的就是「判据实际看过的那一帧」，用来核对识别结果最准。
func (s *Sink) OnNodePipelineNode(ctx *maa.Context, status maa.EventStatus, detail maa.NodePipelineNodeDetail) {
	if status != maa.EventStatusSucceeded || ctx == nil || detail.Name == "" {
		return
	}
	s.maybeCapture(ctx, detail.TaskID, detail.Name, "pipeline")
}

// OnNodeRecognitionNode 忽略识别节点事件。
func (s *Sink) OnNodeRecognitionNode(*maa.Context, maa.EventStatus, maa.NodeRecognitionNodeDetail) {
}

// OnNodeActionNode 忽略动作节点事件。
func (s *Sink) OnNodeActionNode(*maa.Context, maa.EventStatus, maa.NodeActionNodeDetail) {}

// OnNodeNextList 忽略 next 列表事件。
func (s *Sink) OnNodeNextList(*maa.Context, maa.EventStatus, maa.NodeNextListDetail) {}

// OnNodeAction 忽略动作事件。
func (s *Sink) OnNodeAction(*maa.Context, maa.EventStatus, maa.NodeActionDetail) {}

// OnNodeRecognition 处理识别成功事件（配置 event=recognition 时用它，默认）。
func (s *Sink) OnNodeRecognition(ctx *maa.Context, status maa.EventStatus, detail maa.NodeRecognitionDetail) {
	if status != maa.EventStatusSucceeded || ctx == nil || detail.Name == "" {
		return
	}
	s.maybeCapture(ctx, detail.TaskID, detail.Name, "recognition")
}

// maybeCapture 是两类事件的公共实现；from 标记事件来源，与配置的 event 对不上就跳过。
func (s *Sink) maybeCapture(ctx *maa.Context, taskID uint64, name, from string) {
	now := time.Now()
	cfg, patterns, on := s.store.current(now)
	if !on || cfg.Event != from || !matchName(patterns, name) {
		return
	}
	if !s.allow(taskID, name, now, cfg) {
		return
	}

	tasker := ctx.GetTasker()
	if tasker == nil {
		return
	}
	ctrl := tasker.GetController()
	if ctrl == nil {
		return
	}

	img, err := ctrl.CacheImageInto(nil)
	if err != nil || img == nil {
		log.Debug().
			Err(err).
			Str("component", component).
			Str("node", name).
			Msg("cached image unavailable, skip ui capture")
		return
	}

	stem := now.Format("20060102-150405.000") + "_" + safeName(name)
	path := filepath.Join(fsutil.UserDataPath("debug", cfg.Dir), stem+"."+cfg.Format)
	if err := writeImage(path, img, cfg); err != nil {
		log.Warn().
			Err(err).
			Str("component", component).
			Str("node", name).
			Str("path", path).
			Msg("failed to save ui capture")
		return
	}

	log.Info().
		Str("component", component).
		Str("node", name).
		Str("event", from).
		Str("path", path).
		Int("width", img.Bounds().Dx()).
		Int("height", img.Bounds().Dy()).
		Msg("ui capture saved")
}

// allow 判断这次是否该保存，并在放行时立刻记账，避免并发事件重复存同一节点。
func (s *Sink) allow(taskID uint64, name string, now time.Time, cfg Config) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.taskID != taskID {
		s.taskID = taskID
		s.saved = make(map[string]int)
		s.lastAt = make(map[string]time.Time)
	}

	if cfg.oncePerNode() {
		if s.saved[name] > 0 {
			return false
		}
	} else if last, ok := s.lastAt[name]; ok && now.Sub(last) < cfg.cooldown() {
		return false
	}

	if s.total >= cfg.Max {
		if !s.capped {
			s.capped = true
			log.Warn().
				Str("component", component).
				Int("max", cfg.Max).
				Msg("ui capture limit reached for this process, raise max in the config to keep going")
		}
		return false
	}

	s.saved[name]++
	s.lastAt[name] = now
	s.total++
	return true
}

// writeImage 先建目录再落盘。JPEG 体积约为 PNG 的八分之一，默认用它。
func writeImage(path string, img image.Image, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Str("component", component).Str("path", path).Msg("failed to close ui capture")
		}
	}()

	if cfg.Format == "png" {
		enc := png.Encoder{CompressionLevel: png.BestSpeed}
		return enc.Encode(f, img)
	}
	return jpeg.Encode(f, img, &jpeg.Options{Quality: cfg.Quality})
}

// safeName 把节点名变成安全的文件名，保留可读性。
func safeName(name string) string {
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_",
		"?", "_", "\"", "_", "<", "_", ">", "_", "|", "_",
		"\n", "_", "\t", "_", " ", "_",
	)
	out := replacer.Replace(name)
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}
