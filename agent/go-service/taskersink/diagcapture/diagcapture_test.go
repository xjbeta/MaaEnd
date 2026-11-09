package diagcapture

import (
	"image"
	"strings"
	"testing"
)

// nearLine 决定「正常落空」不会被留证、「贴线」一定会被留证。
func TestNearLine(t *testing.T) {
	cases := []struct {
		name     string
		hit      bool
		margin   float64
		wantNear bool
		why      string
	}{
		{"命中贴线", true, 0.007, true, "CargoBid 一类只剩 0.007 余量的图"},
		{"命中余量临界", true, 0.05, true, "等于容差也要留证"},
		{"命中余量充足", true, 0.06, false, "真同源位置恒为 ±1.0000，不该留证"},
		{"落空差一点", false, -0.04, true, "最高分离阈值只差 0.04"},
		{"落空临界", false, -0.05, true, "等于容差也要留证"},
		{"落空差很多", false, -0.06, false, "已经超出容差"},
		{"正常否定区间", false, -0.40, false, "0.4~0.6 是正常否定，绝不能刷屏"},
		{"花屏幕的常见落空", false, -0.096, false, "RewardHeader 0.601/0.70、MapMind 0.60/0.70 这类，收紧容差后不再留证"},
		{"落空却越过阈值", false, 0.02, false, "被后续过滤掉的结果不是贴线"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nearLine(c.hit, c.margin); got != c.wantNear {
				t.Fatalf("nearLine(%v, %v) = %v, want %v (%s)", c.hit, c.margin, got, c.wantNear, c.why)
			}
		})
	}
}

// parseNodeJSON 要能吃下本仓库的两种写法：内联子识别与节点引用。
func TestParseNodeJSON(t *testing.T) {
	raw := `{
		"recognition": "And",
		"all_of": [
			{
				"recognition": "TemplateMatch",
				"roi": [-350, 0, 350, 60],
				"template": ["SceneManager/MapMind.png"],
				"method": 10001
			},
			"InWorld",
			{
				"recognition": "TemplateMatch",
				"template": ["SceneManager/MapMissionReminder.png"],
				"threshold": 0.9
			}
		]
	}`

	spec := parseNodeJSON(raw)
	if spec == nil {
		t.Fatal("parseNodeJSON returned nil")
	}
	if spec.Type != "And" {
		t.Fatalf("Type = %q, want And", spec.Type)
	}
	if len(spec.Children) != 3 {
		t.Fatalf("children = %d, want 3", len(spec.Children))
	}
	if got := spec.threshold(); got != defaultThreshold {
		t.Fatalf("And 节点自身阈值 = %v, want 不适用时的默认值 %v", got, defaultThreshold)
	}

	first := spec.Children[0]
	if len(first.Templates) != 1 || first.Templates[0] != "SceneManager/MapMind.png" {
		t.Fatalf("first templates = %v", first.Templates)
	}
	if got := first.threshold(); got != defaultThreshold {
		t.Fatalf("缺省阈值 = %v, want %v", got, defaultThreshold)
	}

	if spec.Children[1].Node != "InWorld" {
		t.Fatalf("节点引用 = %q, want InWorld", spec.Children[1].Node)
	}

	if got := spec.Children[2].threshold(); got != 0.9 {
		t.Fatalf("显式阈值 = %v, want 0.9", got)
	}
}

// 旧式嵌套写法（recognition 是对象）也要能解析。
func TestParseNodeJSONNestedParam(t *testing.T) {
	spec := parseNodeJSON(`{"recognition":{"type":"TemplateMatch","param":{"template":["A.png","B.png"],"threshold":[0.8,0.6]}}}`)
	if spec == nil || spec.Type != "TemplateMatch" {
		t.Fatalf("spec = %#v", spec)
	}
	// 多模板取最小阈值，避免漏掉只在宽松那条线上贴线的样本。
	if got := spec.threshold(); got != 0.6 {
		t.Fatalf("threshold = %v, want 0.6", got)
	}
}

// 阈值数组短于模板数量时按最后一个值补齐，不留 0 值。
func TestToThresholdsPadding(t *testing.T) {
	got := toThresholds([]any{0.85}, 3)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, v := range got {
		if v != 0.85 {
			t.Fatalf("got[%d] = %v, want 0.85", i, v)
		}
	}

	if got := toThresholds(nil, 2); len(got) != 2 || got[0] != defaultThreshold || got[1] != defaultThreshold {
		t.Fatalf("缺省阈值 = %v", got)
	}
}

// 同一帧画面要得到同一个指纹，画面变了指纹要变。
func TestFrameSignature(t *testing.T) {
	blank := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	same := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	if frameSignature(blank) != frameSignature(same) {
		t.Fatal("相同画面得到了不同指纹")
	}

	other := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	for y := 300; y < 420; y++ {
		for x := 500; x < 780; x++ {
			other.Set(x, y, image.White)
		}
	}
	if frameSignature(blank) == frameSignature(other) {
		t.Fatal("画面变化没有反映到指纹上")
	}
}

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"InMapAny/all_of[0]": "InMapAny-all_of-0",
		"":                   "unknown",
		"正常节点":               "unknown",
	}
	for in, want := range cases {
		if got := safeName(in); got != want {
			t.Fatalf("safeName(%q) = %q, want %q", in, got, want)
		}
	}

	// 文件名片段不能带路径分隔符。
	if name := safeName("a/b\\c"); strings.ContainsAny(name, `/\`) {
		t.Fatalf("safeName 泄漏了路径分隔符: %q", name)
	}
}

func TestSidecarStem(t *testing.T) {
	f := finding{Node: "InMapAny", Path: "InMapAny/all_of[0]"}
	if got := sidecarStem(f); got != "InMapAny-all_of-0" {
		t.Fatalf("sidecarStem = %q", got)
	}
	if got := sidecarStem(finding{Node: "InMapAny", Path: "InMapAny"}); got != "InMapAny" {
		t.Fatalf("sidecarStem = %q", got)
	}
}
