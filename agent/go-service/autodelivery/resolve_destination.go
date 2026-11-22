package autodelivery

import (
	"errors"
	"regexp"

	"github.com/MaaXYZ/MaaEnd/agent/go-service/pkg/i18n"
	"github.com/MaaXYZ/MaaEnd/agent/go-service/pkg/maafocus"
	maa "github.com/MaaXYZ/maa-framework-go/v4"
	"github.com/rs/zerolog/log"
)

const (
	resolveDestinationActionName = "AutoDeliveryResolveDestinationAction"
	navigateDestinationNode      = "AutoDeliveryNavigateDestination"
	retryNavigateDestinationNode = "AutoDeliveryRetryNavigateDestination"
	afterResolveDestinationNode  = "AutoDeliveryAfterResolveDestination"
	areaTextNode                 = "AutoDeliveryCheckAreaText"
	destinationTextNode          = "AutoDeliveryCheckDestinationText"
	submitGoodsNode              = "AutoDeliverySubmitGoods"
	submitGoodsTargetNode        = "AutoDeliveryCheckSubmitGoodsTarget"
	submitGoodsButtonNode        = "AutoDeliveryCheckSubmitGoodsButton"
	submitGoodsNameNode          = "AutoDeliveryCheckSubmitGoodsName"
	checkSkipChatReadyNode       = "AutoDeliveryCheckSkipChatReady"
	closeRewardDialogNode        = "AutoDeliveryCloseRewardDialog"
	// destinationZiplineRequiredFocusKey 是「只能坐滑索抵达、但用户选择步行」时讲给用户的原因。
	destinationZiplineRequiredFocusKey = "autodelivery.focus.destination_zipline_required"
)

// AutoDeliveryResolveDestinationAction 根据 Pipeline OCR 文本或已确认的终点 ID 选择对应的生成路线节点。
type AutoDeliveryResolveDestinationAction struct{}

var _ maa.CustomActionRunner = &AutoDeliveryResolveDestinationAction{}

// Run 读取 Pipeline 提供的 OCR 结果或精确终点 ID，并更新终点导航节点。
func (a *AutoDeliveryResolveDestinationAction) Run(ctx *maa.Context, arg *maa.CustomActionArg) bool {
	if ctx == nil || arg == nil || arg.RecognitionDetail == nil {
		log.Error().
			Str("component", resolveDestinationActionName).
			Msg("action context or recognition detail is missing")
		return false
	}

	selection, err := parseDestinationSelection(arg.CustomActionParam)
	if err != nil {
		log.Error().
			Err(err).
			Str("component", resolveDestinationActionName).
			Msg("failed to parse action parameters")
		return false
	}
	options, err := loadNavigationOptions(ctx, navigateDestinationNode)
	if err != nil {
		log.Error().
			Err(err).
			Str("component", resolveDestinationActionName).
			Str("node", navigateDestinationNode).
			Msg("failed to load navigation options")
		return false
	}

	var (
		areaText        string
		destinationText string
		dest            destination
		match           destinationMatch
		resolveErr      error
	)
	if selection.DestinationID != "" {
		dest, resolveErr = getDestination(selection.DestinationID)
	} else {
		var combined bool
		areaText, destinationText, combined = destinationOCRFields(arg.RecognitionDetail)
		if combined {
			dest, match, resolveErr = resolveDestinationByArea(areaText, destinationText)
		} else {
			destinationText, resolveErr = recognitionText(arg.RecognitionDetail)
			if resolveErr == nil {
				dest, match, resolveErr = resolveDestination(destinationText)
			}
		}
	}
	if resolveErr != nil {
		var ambiguity *recycleBinAmbiguityError
		if errors.As(resolveErr, &ambiguity) {
			if err := ctx.OverridePipeline(buildRecycleBinResolutionOverride(ambiguity.AreaID)); err != nil {
				log.Error().
					Err(err).
					Str("component", resolveDestinationActionName).
					Str("area", ambiguity.AreaID).
					Msg("failed to configure recycle bin map resolution")
				return false
			}

			candidateIDs := make([]string, 0, len(ambiguity.Candidates))
			for _, candidate := range ambiguity.Candidates {
				candidateIDs = append(candidateIDs, candidate.ID)
			}
			log.Info().
				Str("component", resolveDestinationActionName).
				Str("areaText", areaText).
				Str("destinationText", destinationText).
				Str("area", ambiguity.AreaID).
				Strs("candidates", candidateIDs).
				Msg("delivery recycle bin needs map resolution")
			return true
		}

		log.Error().
			Err(resolveErr).
			Str("component", resolveDestinationActionName).
			Str("requestedDestination", selection.DestinationID).
			Str("areaText", areaText).
			Str("destinationText", destinationText).
			Float64("similarity", match.Similarity).
			Float64("runnerUpSimilarity", match.RunnerUpSimilarity).
			Float64("areaSimilarity", match.AreaSimilarity).
			Float64("areaRunnerUpSimilarity", match.AreaRunnerUp).
			Msg("failed to resolve delivery destination")
		return false
	}
	// 只能坐滑索抵达的终点没有可用步行路线，用户选择步行时必须直接失败，不能硬走。
	if !ensureZiplineSelected(ctx, dest.ZiplineOnly, options.Zip, destinationZiplineRequiredFocusKey, destinationDisplayName(dest)) {
		log.Error().
			Str("component", resolveDestinationActionName).
			Str("destination", dest.ID).
			Str("depot", dest.DepotID).
			Msg("delivery destination is zipline-only but navigation is configured to walk")
		return false
	}
	if err := ctx.OverridePipeline(buildDestinationNavigationOverride(dest, options.Zip)); err != nil {
		log.Error().
			Err(err).
			Str("component", resolveDestinationActionName).
			Str("destination", dest.ID).
			Msg("failed to inject delivery navigation parameters")
		return false
	}
	maafocus.Print(ctx, i18n.T("autodelivery.focus.destination_resolved", destinationDisplayName(dest)))

	log.Info().
		Str("component", resolveDestinationActionName).
		Str("requestedDestination", selection.DestinationID).
		Str("areaText", areaText).
		Str("destinationText", destinationText).
		Str("destination", dest.ID).
		Str("depot", dest.DepotID).
		Str("matchedArea", match.AreaText).
		Str("matchedDestination", match.DestinationText).
		Str("matchedObjective", match.ObjectiveText).
		Float64("similarity", match.Similarity).
		Float64("runnerUpSimilarity", match.RunnerUpSimilarity).
		Float64("areaSimilarity", match.AreaSimilarity).
		Float64("areaRunnerUpSimilarity", match.AreaRunnerUp).
		Str("area", dest.AreaID).
		Bool("zip", options.Zip).
		Bool("walkOnly", dest.WalkOnly).
		Bool("verifyName", dest.VerifyName).
		Str("routeNode", selectRouteNode(dest.RouteNode, dest.ZipRouteNode, options.Zip, dest.WalkOnly)).
		Str("retryRouteNode", dest.RetryRouteNode).
		Msg("resolved delivery job destination")
	return true
}

func destinationDisplayName(dest destination) string {
	if dest.Kind == destinationKindRecycleBin {
		areaName := localizedName(dest.AreaNames, dest.AreaID)
		return i18n.T("autodelivery.destination.recycle_bin", areaName, dest.SerialID)
	}
	return localizedName(dest.Names, dest.ID)
}

func buildDestinationNavigationOverride(dest destination, zip bool) map[string]any {
	override := map[string]any{
		afterResolveDestinationNode: map[string]any{
			"next": defaultDestinationFlow(),
		},
		navigateDestinationNode: map[string]any{
			"custom_action": "SubTask",
			"custom_action_param": map[string]any{
				"sub": []string{selectRouteNode(dest.RouteNode, dest.ZipRouteNode, zip, dest.WalkOnly)},
			},
		},
		retryNavigateDestinationNode: map[string]any{
			"enabled": false,
		},
	}
	if dest.RetryRouteNode != "" {
		override[retryNavigateDestinationNode] = map[string]any{
			"enabled":       true,
			"custom_action": "SubTask",
			"custom_action_param": map[string]any{
				"sub": []string{dest.RetryRouteNode},
			},
		}
	}

	// 提交货物后的画面由终点类型决定：NPC 终点先进入对话，资源回收站终点直接弹出奖励结算框。
	// 两个画面同时挂在 next 里时，另一类终点的识别框有机会抢先命中，所以这里按终点类型写死单一路径。
	//
	// NPC 终点另挂一条 on_error 兜底：next 循环超时（对话始终没出现）后才认奖励结算框。
	// on_error 只在 next 全部落空并耗尽 timeout 时触发，正常出对话的流程不会走到这里，
	// 因此不会把「抢先命中」放回来。2026-11-22 10:33 实机：NPC 终点「赵昭」提交后没进对话，
	// 直接弹奖励框（10:33:43.344 AutoDeliveryCloseRewardDialog 命中），而 next 只有
	// AutoDeliveryCheckSkipChatReady，空转 6019 ms 后 PipelineTask bad next，整单判失败
	// ——货物其实已送达、奖励也已入账。资源回收站终点的 next 本身就是奖励框，不需要兜底。
	submitGoods := map[string]any{
		"next": submitGoodsFlow(dest.Kind),
	}
	if dest.Kind != destinationKindRecycleBin {
		submitGoods["on_error"] = []string{closeRewardDialogNode}
	}
	override[submitGoodsNode] = submitGoods

	// 交货图标只有十几像素，周围还有别的角色时容易命中别人的交互提示。
	// 开启名称复核的终点额外要求提示文本命中该终点的名称；每次解析都显式重写 all_of，
	// 避免上一个终点开启的复核残留到本次流程。
	submitGoodsAllOf := []string{submitGoodsButtonNode}
	if dest.VerifyName {
		submitGoodsAllOf = append(submitGoodsAllOf, submitGoodsNameNode)
		override[submitGoodsNameNode] = map[string]any{
			"expected": nameExpectations(dest),
		}
	}
	override[submitGoodsTargetNode] = map[string]any{
		"all_of": submitGoodsAllOf,
	}
	return override
}

// nameExpectations 把终点的各语言名称转成正则字面量：交互提示里的名称随游戏语言变化，
// 而 OCR 节点的 expected 按正则匹配，名称里的正则元字符必须转义。
func nameExpectations(dest destination) []string {
	names := localizedTexts(dest.Names)
	expected := make([]string, 0, len(names))
	for _, name := range names {
		expected = append(expected, regexp.QuoteMeta(name))
	}
	return expected
}

func buildRecycleBinResolutionOverride(areaID string) map[string]any {
	return map[string]any{
		afterResolveDestinationNode: map[string]any{
			"next": []string{
				"AutoDeliveryViewRecycleBin" + areaID + "Map",
				"AutoDeliveryStartTrackingRecycleBin" + areaID,
			},
		},
	}
}

func defaultDestinationFlow() []string {
	return []string{
		"AutoDeliveryReturnWorldAndNavigateDestination",
	}
}

// submitGoodsFlow 返回提交货物后要先识别的画面：
// 资源回收站终点没有对话，直接出奖励结算框；NPC 终点先进入对话，等跳过按钮稳定再点。
func submitGoodsFlow(kind string) []string {
	if kind == destinationKindRecycleBin {
		return []string{closeRewardDialogNode}
	}
	return []string{checkSkipChatReadyNode}
}
