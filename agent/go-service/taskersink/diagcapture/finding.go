package diagcapture

import (
	"sort"
	"strings"
	"time"

	maa "github.com/MaaXYZ/maa-framework-go/v4"
)

// candidate 是识别结果里的一个候选框及其分数。
type candidate struct {
	Box   maa.Rect `json:"box"`
	Score float64  `json:"score"`
}

// finding 是一次贴线的模板匹配。
type finding struct {
	// Node 是本条留证所属的 pipeline 节点。
	Node string
	// Path 是节点内部到该模板匹配的路径，例如 "InMapAny/all_of[0]"。
	Path string
	// Templates 与 Threshold 来自节点定义。
	Templates []string
	Threshold float64
	// Hit 表示框架是否判为命中，Score 是命中分或最高分。
	Hit   bool
	Score float64
	Box   maa.Rect
	// Candidates 是识别结果里的全部候选分，按分数降序。
	Candidates []candidate
	// RecoID 是该次识别的 reco_id，便于回查 maafw.log。
	RecoID int64
}

// key 是冷却与去重使用的标识：同一节点上的同一组模板视为同一项。
func (f *finding) key() string {
	return f.Node + "|" + strings.Join(f.Templates, ",")
}

// margin 返回分数相对阈值的余量，负数表示还差多少。
func (f *finding) margin() float64 {
	return f.Score - f.Threshold
}

// reason 是写进 sidecar 的判定原因。
func (f *finding) reason() string {
	if f.Hit {
		return "near_hit"
	}
	return "near_miss"
}

// sidecar 是落盘的判定记录。
type sidecar struct {
	Timestamp  string      `json:"timestamp"`
	Reason     string      `json:"reason"`
	TaskID     uint64      `json:"task_id"`
	RecoID     int64       `json:"reco_id"`
	Node       string      `json:"node"`
	NodePath   string      `json:"node_path"`
	Algorithm  string      `json:"algorithm"`
	Templates  []string    `json:"templates"`
	Threshold  float64     `json:"threshold"`
	Hit        bool        `json:"hit"`
	Score      float64     `json:"score"`
	Margin     float64     `json:"margin"`
	Box        maa.Rect    `json:"box"`
	Candidates []candidate `json:"candidates,omitempty"`
	Image      string      `json:"image"`
	Controller string      `json:"controller"`
	Resolution [2]int      `json:"resolution"`
}

func (f *finding) sidecar(image string, taskID uint64, controller string, resolution [2]int, at time.Time) sidecar {
	candidates := f.Candidates
	if len(candidates) > maxCandidates {
		candidates = candidates[:maxCandidates]
	}
	return sidecar{
		Timestamp:  at.Format(time.RFC3339Nano),
		Reason:     f.reason(),
		TaskID:     taskID,
		RecoID:     f.RecoID,
		Node:       f.Node,
		NodePath:   f.Path,
		Algorithm:  string(maa.RecognitionTypeTemplateMatch),
		Templates:  f.Templates,
		Threshold:  f.Threshold,
		Hit:        f.Hit,
		Score:      f.Score,
		Margin:     f.margin(),
		Box:        f.Box,
		Candidates: candidates,
		Image:      image,
		Controller: controller,
		Resolution: resolution,
	}
}

// judge 判断一次 TemplateMatch 是否贴线。
//
// 命中时看命中分与阈值的余量，落空时看最高分与阈值的差距；
// 两种情况的容差都远小于「元素不在画面」时的落差，因此不会把正常落空算进来。
func judge(spec *nodeSpec, reco *maa.RecognitionDetail, root, path string) (finding, bool) {
	if spec == nil || reco == nil || reco.Results == nil {
		return finding{}, false
	}
	// inverse 节点的「命中」含义正好相反，贴线判定没有意义。
	if spec.Inverse {
		return finding{}, false
	}

	f := finding{
		Node:      root,
		Path:      path,
		Templates: spec.Templates,
		Threshold: spec.threshold(),
		RecoID:    reco.ID,
	}

	top := 0.0
	for _, item := range reco.Results.All {
		if item == nil {
			continue
		}
		res, ok := item.AsTemplateMatch()
		if !ok {
			continue
		}
		f.Candidates = append(f.Candidates, candidate{Box: res.Box, Score: res.Score})
		if res.Score > top {
			top = res.Score
		}
	}
	sort.SliceStable(f.Candidates, func(i, j int) bool {
		return f.Candidates[i].Score > f.Candidates[j].Score
	})

	if best := reco.Results.Best; best != nil {
		if res, ok := best.AsTemplateMatch(); ok {
			f.Hit = true
			f.Score = res.Score
			f.Box = res.Box
		}
	}
	if !f.Hit {
		f.Score = top
		if len(f.Candidates) > 0 {
			f.Box = f.Candidates[0].Box
		}
	}

	margin := f.margin()
	return f, nearLine(f.Hit, margin)
}

// nearLine 是留证的全部判据，单独抽出来便于验证「判断的界限」。
//
//   - 命中：余量不超过 nearHitMargin 即贴线；
//   - 落空：还差不超过 nearMissMargin 才算差一点，分数越过阈值（被后续过滤）不算。
func nearLine(hit bool, margin float64) bool {
	if hit {
		return margin <= nearHitMargin
	}
	return margin < 0 && -margin <= nearMissMargin
}
