// Package diagcapture 在模板匹配贴近判定线时，把当时的画面留证到日志目录。
//
// 只判断 TemplateMatch 的结果，两种情况才留证：
//   - 命中，但命中分只比阈值高一点点（阈值或图片随时可能失效）；
//   - 落空，但最高分离阈值只差一点点（差一点就命中）。
//
// 元素本来就不在画面上时，最高分通常落在 0.4~0.6，距阈值远大于下面的容差，
// 因此正常落空不会产生任何文件；再叠加同一组模板的冷却时间与单次进程上限，
// 长时间挂机也不会刷屏。
package diagcapture

import (
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MaaXYZ/MaaEnd/agent/go-service/pkg/fsutil"
	"github.com/MaaXYZ/MaaEnd/agent/go-service/pkg/pienv"
	maa "github.com/MaaXYZ/maa-framework-go/v4"
	"github.com/rs/zerolog/log"
)

const (
	component = "diagcapture"

	// nearHitMargin 命中分与阈值的最大余量；不超过即视为贴线命中。
	nearHitMargin = 0.05
	// nearMissMargin 落空时最高分与阈值的最大差距；不超过即视为差一点。
	//
	// 实测一次完整跑（4 份 maafw.log，约 6900 个节点定义、近 7000 次节点识别）：
	// 容差取 0.10 会存 43 张，其中多数是「元素不在场但画面很花」的 0.60 分
	// （RewardHeader 0.601/0.70、MapMind 0.60/0.70）；收紧到 0.05 后只剩 19 张，
	// 全部是真贴线（CargoBid 0.774/0.80、ElasticGoodsButtonSide 0.781/0.80 等）。
	nearMissMargin = 0.05

	// perKeyCooldown 同一节点同一组模板的最短留证间隔。
	perKeyCooldown = 15 * time.Second
	// frameReuseWindow 画面特征一致时复用同一张截图的窗口。
	frameReuseWindow = 3 * time.Second
	// maxCaptures 单个进程生命周期内最多保存的截图数量。
	maxCaptures = 120
	// maxCandidates 写进 sidecar 的候选分数量上限。
	maxCandidates = 10

	// captureDir 截图与 sidecar 的落盘目录，与 go-service.log 同级。
	captureDir = "capture"
)

// frameSigLen 是画面指纹的字节数（9×8 灰度格子，量化到 16 级）。
const frameSigLen = 9 * 8

var _ maa.ContextEventSink = (*Sink)(nil)

// Sink 监听节点识别结果并留证贴线的模板匹配。
type Sink struct {
	mu sync.Mutex

	// kind 缓存判定出的控制器类型，避免每次事件都去问框架。
	kind string

	// nodeCache 缓存节点定义，nil 值表示已确认取不到，不再重复请求。
	nodeCache map[string]*nodeSpec
	// lastKey 记录每组模板上次留证的时间，用于冷却。
	lastKey map[string]time.Time

	// captures 已保存的截图数量；capped 记录是否已经提示过上限。
	captures int
	capped   bool

	// 上一张截图的画面指纹与文件名，用于同帧复用。
	sig      [frameSigLen]byte
	sigOK    bool
	sigAt    time.Time
	lastStem string
}

// NewSink 创建留证 sink。
func NewSink() *Sink {
	return &Sink{
		nodeCache: make(map[string]*nodeSpec),
		lastKey:   make(map[string]time.Time),
	}
}

// 其余 Context 事件与本功能无关，空实现即可。

// OnNodePipelineNode 忽略节点执行事件。
func (s *Sink) OnNodePipelineNode(*maa.Context, maa.EventStatus, maa.NodePipelineNodeDetail) {}

// OnNodeRecognitionNode 忽略识别节点事件。
func (s *Sink) OnNodeRecognitionNode(*maa.Context, maa.EventStatus, maa.NodeRecognitionNodeDetail) {}

// OnNodeActionNode 忽略动作节点事件。
func (s *Sink) OnNodeActionNode(*maa.Context, maa.EventStatus, maa.NodeActionNodeDetail) {}

// OnNodeNextList 忽略 next 列表事件。
func (s *Sink) OnNodeNextList(*maa.Context, maa.EventStatus, maa.NodeNextListDetail) {}

// OnNodeAction 忽略动作事件。
func (s *Sink) OnNodeAction(*maa.Context, maa.EventStatus, maa.NodeActionDetail) {}

// OnNodeRecognition 处理节点识别事件。
func (s *Sink) OnNodeRecognition(ctx *maa.Context, status maa.EventStatus, detail maa.NodeRecognitionDetail) {
	if ctx == nil || detail.Name == "" || detail.RecognitionID == 0 {
		return
	}
	if status != maa.EventStatusFailed && status != maa.EventStatusSucceeded {
		return
	}

	tasker := ctx.GetTasker()
	if tasker == nil {
		return
	}
	ctrl := tasker.GetController()
	if ctrl == nil || !s.enabled(ctrl) {
		return
	}

	reco, err := tasker.GetRecognitionDetail(int64(detail.RecognitionID))
	if err != nil || reco == nil {
		log.Debug().
			Err(err).
			Str("component", component).
			Str("node", detail.Name).
			Msg("recognition detail unavailable, skip diagnostic capture")
		return
	}
	if reco.Name == "" {
		reco.Name = detail.Name
	}

	findings := s.inspect(ctx, reco)
	if len(findings) == 0 {
		return
	}
	s.capture(ctrl, detail.TaskID, findings)
}

// maxWalkDepth 限制遍历深度，避免节点定义里出现互相引用时无限递归。
const maxWalkDepth = 8

// inspect 递归解析识别结果，返回所有贴线的模板匹配。
func (s *Sink) inspect(ctx *maa.Context, reco *maa.RecognitionDetail) []finding {
	spec := s.loadNode(ctx, reco.Name)
	if spec == nil {
		return nil
	}
	var out []finding
	s.walk(ctx, spec, reco, reco.Name, reco.Name, 0, &out)
	return out
}

func (s *Sink) walk(ctx *maa.Context, spec *nodeSpec, reco *maa.RecognitionDetail, root, path string, depth int, out *[]finding) {
	if reco == nil || depth > maxWalkDepth {
		return
	}
	if spec != nil && spec.Node != "" {
		s.walk(ctx, s.loadNode(ctx, spec.Node), reco, root, path+"/"+spec.Node, depth+1, out)
		return
	}

	switch strings.ToLower(reco.Algorithm) {
	case "and", "or":
		childKey := "all_of"
		if strings.EqualFold(reco.Algorithm, "or") {
			childKey = "any_of"
		}
		for i, sub := range reco.CombinedResult {
			if sub == nil {
				continue
			}
			var child *nodeSpec
			if spec != nil && i < len(spec.Children) {
				child = spec.Children[i]
			}
			// 节点引用型的子识别会带上节点名，按名字对齐比按位置更可靠。
			if child == nil || (child.Node != "" && sub.Name != "" && sub.Name != child.Node) {
				if alt := findRef(spec, sub.Name); alt != nil {
					child = alt
				}
			}
			// 位置对不上又没在 all_of/any_of 里找到时，退化成按子识别自己的名字取节点定义。
			if child == nil && sub.Name != "" {
				child = s.loadNode(ctx, sub.Name)
			}
			s.walk(ctx, child, sub, root, fmt.Sprintf("%s/%s[%d]", path, childKey, i), depth+1, out)
		}
	case "templatematch":
		if f, ok := judge(spec, reco, root, path); ok {
			*out = append(*out, f)
		}
	}
}

func findRef(spec *nodeSpec, name string) *nodeSpec {
	if spec == nil || name == "" {
		return nil
	}
	for _, child := range spec.Children {
		if child != nil && child.Node == name {
			return child
		}
	}
	return nil
}

// loadNode 取节点定义并缓存；取不到时缓存 nil，避免重复请求框架。
func (s *Sink) loadNode(ctx *maa.Context, name string) *nodeSpec {
	if ctx == nil || name == "" {
		return nil
	}

	s.mu.Lock()
	cached, seen := s.nodeCache[name]
	s.mu.Unlock()
	if seen {
		return cached
	}

	var spec *nodeSpec
	raw, err := ctx.GetNodeJSON(name)
	if err != nil {
		log.Debug().
			Err(err).
			Str("component", component).
			Str("node", name).
			Msg("node json unavailable, diagnostic capture skips this node")
	} else {
		spec = parseNodeJSON(raw)
	}

	s.mu.Lock()
	s.nodeCache[name] = spec
	s.mu.Unlock()
	return spec
}

// enabled 判断当前是否是需要留证的控制器（安卓/移动端），结果缓存。
func (s *Sink) enabled(ctrl *maa.Controller) bool {
	s.mu.Lock()
	kind := s.kind
	s.mu.Unlock()
	if kind != "" {
		return pienv.IsAdbLikeControllerType(kind)
	}

	kind = pienv.ResolveControllerKind(ctrl)
	if kind == pienv.ControllerKindUnknown {
		return false
	}

	s.mu.Lock()
	s.kind = kind
	s.mu.Unlock()
	return pienv.IsAdbLikeControllerType(kind)
}

// capture 保存当前画面并写出 sidecar。
func (s *Sink) capture(ctrl *maa.Controller, taskID uint64, findings []finding) {
	now := time.Now()

	ready := make([]finding, 0, len(findings))
	s.mu.Lock()
	for _, f := range findings {
		key := f.key()
		if last, ok := s.lastKey[key]; ok && now.Sub(last) < perKeyCooldown {
			continue
		}
		ready = append(ready, f)
	}
	for _, f := range ready {
		s.lastKey[f.key()] = now
	}
	s.mu.Unlock()
	if len(ready) == 0 {
		return
	}

	img, err := ctrl.CacheImageInto(nil)
	if err != nil || img == nil {
		log.Debug().
			Err(err).
			Str("component", component).
			Msg("cached image unavailable, skip diagnostic capture")
		return
	}

	sig := frameSignature(img)
	stem := now.Format("20060102-150405.000")

	s.mu.Lock()
	reuse := s.sigOK && s.lastStem != "" && sig == s.sig && now.Sub(s.sigAt) <= frameReuseWindow
	if !reuse {
		if s.captures >= maxCaptures {
			if !s.capped {
				s.capped = true
				log.Warn().
					Str("component", component).
					Int("max", maxCaptures).
					Str("dir", fsutil.UserDataPath("debug", captureDir)).
					Msg("diagnostic capture limit reached for this run")
			}
			s.mu.Unlock()
			return
		}
		s.captures++
		s.sig = sig
		s.sigOK = true
		s.sigAt = now
		s.lastStem = stem
	} else {
		stem = s.lastStem
	}
	s.mu.Unlock()

	dir := fsutil.UserDataPath("debug", captureDir)
	if !reuse {
		path := filepath.Join(dir, stem+".png")
		if err := writePNG(path, img); err != nil {
			log.Warn().
				Err(err).
				Str("component", component).
				Str("path", path).
				Msg("failed to save diagnostic capture")
			return
		}
	}

	bounds := img.Bounds()
	for _, f := range ready {
		record := f.sidecar(stem+".png", taskID, s.controllerName(), [2]int{bounds.Dx(), bounds.Dy()}, now)
		data, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			continue
		}
		path := filepath.Join(dir, stem+"_"+sidecarStem(f)+".json")
		if err := fsutil.WriteFileAtomic(path, append(data, '\n'), 0o644); err != nil {
			log.Warn().
				Err(err).
				Str("component", component).
				Str("path", path).
				Msg("failed to save diagnostic sidecar")
			continue
		}
		log.Info().
			Str("component", component).
			Str("node", f.Node).
			Str("path", f.Path).
			Strs("templates", f.Templates).
			Float64("threshold", f.Threshold).
			Float64("score", f.Score).
			Float64("margin", f.margin()).
			Bool("hit", f.Hit).
			Str("image", stem+".png").
			Msg("diagnostic capture saved")
	}
}

func (s *Sink) controllerName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.kind
}

// writePNG 以最小压缩写 PNG：这些图只用于诊断，编码耗时比体积更重要。
func writePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(f, img); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// frameSignature 计算 9×8 灰度降采样指纹并量化到 16 级，
// 用来判断两次留证是否落在同一帧画面上（同帧只保存一张图）。
func frameSignature(img *image.RGBA) [frameSigLen]byte {
	var sig [frameSigLen]byte
	if img == nil {
		return sig
	}

	const cols, rows = 9, 8
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return sig
	}

	for gy := 0; gy < rows; gy++ {
		for gx := 0; gx < cols; gx++ {
			x0 := b.Min.X + w*gx/cols
			x1 := b.Min.X + w*(gx+1)/cols
			y0 := b.Min.Y + h*gy/rows
			y1 := b.Min.Y + h*(gy+1)/rows
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if y1 <= y0 {
				y1 = y0 + 1
			}

			var sum, count uint64
			for y := y0; y < y1; y += 2 {
				for x := x0; x < x1; x += 2 {
					i := img.PixOffset(x, y)
					gray := (uint64(img.Pix[i])*299 + uint64(img.Pix[i+1])*587 + uint64(img.Pix[i+2])*114) / 1000
					sum += gray
					count++
				}
			}
			if count == 0 {
				continue
			}
			sig[gy*cols+gx] = byte((sum / count) >> 4)
		}
	}
	return sig
}

// sidecarStem 用节点名加节点内路径拼出 sidecar 的文件名片段，
// 同一帧上多个节点各自留证时不会互相覆盖。
func sidecarStem(f finding) string {
	name := safeName(f.Node)
	if rel := strings.TrimPrefix(f.Path, f.Node); rel != "" {
		name += safeName(rel)
	}
	return name
}

// safeName 把节点名/路径变成可用的文件名片段。
func safeName(s string) string {
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
		if b.Len() >= 60 {
			break
		}
	}
	// 全部字符都不可用时（例如纯中文节点名），裁剪后可能为空，退化成固定片段，
	// 否则 sidecar 会得到 "_.json" 这类无意义的文件名。
	if name := strings.TrimRight(b.String(), "-."); name != "" {
		return name
	}
	return "unknown"
}
