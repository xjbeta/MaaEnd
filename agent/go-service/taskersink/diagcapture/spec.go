package diagcapture

import (
	"encoding/json"
	"strings"

	"github.com/MaaXYZ/MaaEnd/agent/go-service/pkg/jsonclean"
)

// defaultThreshold 是 MaaFramework 对 TemplateMatch 的默认阈值。
const defaultThreshold = 0.7

// nodeSpec 是 pipeline 节点（或内联子识别）里与本功能相关的那部分定义。
//
// 同一个结构同时表示两种来源：
//   - 节点引用：Node 非空，Type/Templates/Children 都为空，使用时再按名字取节点定义；
//   - 内联识别：Type 是识别类型，其余字段按该类型解释。
type nodeSpec struct {
	// Node 是被引用的节点名，仅出现在 And/Or 的子项里。
	Node string
	// Type 是识别类型（"TemplateMatch"、"And"、"Or" 等）。
	Type string
	// Inverse 为真时该识别结果是反的，贴线判定的语义随之改变，直接跳过。
	Inverse bool
	// Templates 是模板路径列表。
	Templates []string
	// Thresholds 与 Templates 等长，已补齐默认值。
	Thresholds []float64
	// Children 是 And 的 all_of 或 Or 的 any_of 子项，顺序与识别结果的子项一一对应。
	Children []*nodeSpec
}

// threshold 返回该识别真正用来判定的阈值。
//
// 多个模板可以各带一个阈值，这里取其中最小的一个：命中与否已经由框架判定，
// 留证只需要判断「离任何一条线都不远」，取最宽松的那条才不会漏掉贴线样本。
func (s *nodeSpec) threshold() float64 {
	if s == nil || len(s.Thresholds) == 0 {
		return defaultThreshold
	}
	th := s.Thresholds[0]
	for _, v := range s.Thresholds[1:] {
		if v < th {
			th = v
		}
	}
	return th
}

// parseSpec 解析一个节点定义或内联子识别定义。
//
// 兼容两种写法：
//   - pipeline v2（本仓库使用）：{"recognition": "TemplateMatch", "template": [...], "threshold": 0.8}
//   - 旧式嵌套：{"recognition": {"type": "TemplateMatch", "param": {...}}}
func parseSpec(raw any) *nodeSpec {
	switch v := raw.(type) {
	case string:
		name := strings.TrimSpace(v)
		if name == "" {
			return nil
		}
		return &nodeSpec{Node: name}
	case map[string]any:
		return parseSpecMap(v)
	default:
		return nil
	}
}

func parseSpecMap(node map[string]any) *nodeSpec {
	spec := &nodeSpec{}
	params := node

	switch reco := node["recognition"].(type) {
	case string:
		spec.Type = strings.TrimSpace(reco)
	case map[string]any:
		if typ, ok := reco["type"].(string); ok {
			spec.Type = strings.TrimSpace(typ)
		}
		if param, ok := reco["param"].(map[string]any); ok {
			merged := make(map[string]any, len(node)+len(param))
			for k, v := range node {
				merged[k] = v
			}
			for k, v := range param {
				merged[k] = v
			}
			params = merged
		}
	}
	if spec.Type == "" {
		return nil
	}

	if inverse, ok := params["inverse"].(bool); ok {
		spec.Inverse = inverse
	}

	spec.Templates = toStringList(params["template"])
	spec.Thresholds = toThresholds(params["threshold"], len(spec.Templates))

	children, ok := params["all_of"].([]any)
	if !ok {
		children, _ = params["any_of"].([]any)
	}
	for _, item := range children {
		if child := parseSpec(item); child != nil {
			spec.Children = append(spec.Children, child)
		}
	}

	return spec
}

// parseNodeJSON 解析 ctx.GetNodeJSON 返回的节点定义。
func parseNodeJSON(raw string) *nodeSpec {
	content := jsonclean.Clean([]byte(raw))
	if len(content) == 0 {
		return nil
	}
	var node map[string]any
	if err := json.Unmarshal(content, &node); err != nil {
		return nil
	}
	return parseSpecMap(node)
}

func toStringList(v any) []string {
	switch val := v.(type) {
	case string:
		if s := strings.TrimSpace(val); s != "" {
			return []string{s}
		}
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	}
	return nil
}

// toThresholds 把 threshold 字段规整成与模板数量等长的列表。
//
// 框架允许 threshold 是单值或与 template 等长的数组；缺省位置补默认阈值，
// 这样后面取最小值时不会因为解析失败而误判。
func toThresholds(v any, count int) []float64 {
	if count <= 0 {
		count = 1
	}
	values := make([]float64, 0, count)

	switch val := v.(type) {
	case float64:
		values = append(values, val)
	case int:
		values = append(values, float64(val))
	case []any:
		for _, item := range val {
			switch n := item.(type) {
			case float64:
				values = append(values, n)
			case int:
				values = append(values, float64(n))
			}
		}
	}

	if len(values) == 0 {
		values = append(values, defaultThreshold)
	}
	for len(values) < count {
		values = append(values, values[len(values)-1])
	}
	return values[:count]
}
