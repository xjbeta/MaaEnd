package retrytask

import (
	"encoding/json"

	"github.com/MaaXYZ/MaaEnd/agent/go-service/common/failurecollector"
	maa "github.com/MaaXYZ/maa-framework-go/v4"
	"github.com/rs/zerolog/log"
)

// RetryTaskAction 把一个子任务跑起来，失败就整块重跑，直到成功或用完次数。
//
// 设计取舍：
//   - 只重试「整块」，不试图从失败点续跑。子任务自身是模块化的（从入口节点重新开始），
//     重跑会走它自己的场景判定，能从当前画面接上就接上，接不上就按它自己的流程回到起点。
//   - 不重试就返回 false 的写法会毁掉「先记失败、最后统一上报」的机制，所以这里
//     无论结果如何都返回 true，真正的成败由 Result 字段体现——调用方需要区分时
//     用 next 里的识别节点，不要指望 action 返回值。
//   - 次数用尽仍然失败时，可选把失败任务记进 FailureCollector，复用已有的汇总链路。
type retryTaskParam struct {
	// Task 要执行的子任务入口节点名，必填
	Task string `json:"task"`
	// Retry 失败后额外重试的次数，不含首次执行。默认 1
	Retry int `json:"retry,omitempty"`
	// DelayMs 每次重试前的等待，给画面/状态一点时间复位。默认 0
	DelayMs int `json:"delay_ms,omitempty"`
	// FailureKey 非空时，全部重试都失败后把 Task 记进 FailureCollector 的这个 key
	FailureKey string `json:"failure_key,omitempty"`
}

// RetryTaskAction 供 Pipeline 调用
type RetryTaskAction struct{}

var _ maa.CustomActionRunner = &RetryTaskAction{}

func (a *RetryTaskAction) Run(ctx *maa.Context, arg *maa.CustomActionArg) bool {
	if ctx == nil || arg == nil {
		log.Error().Str("component", "RetryTaskAction").Msg("nil context or arg")
		return false
	}

	var param retryTaskParam
	if err := json.Unmarshal([]byte(arg.CustomActionParam), &param); err != nil {
		log.Error().Err(err).Str("component", "RetryTaskAction").Str("param", arg.CustomActionParam).Msg("failed to parse params")
		return false
	}
	if param.Task == "" {
		log.Error().Str("component", "RetryTaskAction").Msg("task is required")
		return false
	}
	if param.Retry < 0 {
		param.Retry = 0
	}

	attempts := param.Retry + 1
	for i := 0; i < attempts; i++ {
		if i > 0 {
			log.Warn().
				Str("component", "RetryTaskAction").
				Str("task", param.Task).
				Int("attempt", i+1).
				Int("attempts", attempts).
				Int("delay_ms", param.DelayMs).
				Msg("retrying subtask")
			if param.DelayMs > 0 {
				// 用 pipeline 的等待而不是 sleep，保持与框架节奏一致
				if !waitMs(ctx, param.DelayMs) {
					return false
				}
			}
		}

		detail, err := ctx.RunTask(param.Task)
		if err == nil && detail != nil && detail.Status.Success() {
			if i > 0 {
				log.Info().
					Str("component", "RetryTaskAction").
					Str("task", param.Task).
					Int("attempt", i+1).
					Msg("subtask succeeded after retry")
			}
			return true
		}
		log.Warn().
			Err(err).
			Str("component", "RetryTaskAction").
			Str("task", param.Task).
			Int("attempt", i+1).
			Int("attempts", attempts).
			Msg("subtask attempt failed")
	}

	log.Error().
		Str("component", "RetryTaskAction").
		Str("task", param.Task).
		Int("attempts", attempts).
		Msg("subtask failed after all attempts")

	if param.FailureKey != "" {
		failurecollector.Record(param.FailureKey, param.Task)
	}
	// 全部尝试都失败时返回 false，让本节点失败、任务如实上报失败。
	// 不要为了「不中断流程」返回 true——那会让 App 把失败的任务记成成功。
	return false
}

// waitMs 用框架的空等待节点实现延时，避免在 action 里 sleep 阻塞回调线程。
func waitMs(ctx *maa.Context, ms int) bool {
	const holder = "__RetryTaskWait"
	override := map[string]any{
		holder: map[string]any{
			"pre_delay":  ms,
			"post_delay": 0,
			"rate_limit": 0,
			"action":     "DoNothing",
			"next":       []string{},
		},
	}
	detail, err := ctx.RunTask(holder, override)
	if err != nil || detail == nil {
		log.Warn().Err(err).Str("component", "RetryTaskAction").Msg("wait node failed, continuing without delay")
		return true
	}
	return true
}
