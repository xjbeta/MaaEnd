package autostockpile

import (
	"fmt"
	"image"

	maa "github.com/MaaXYZ/maa-framework-go/v4"
	"github.com/rs/zerolog/log"
)

// maxShelfPages 是货架扫描的页数上限（含首屏）。ADB 每屏可见 6 列 × 2 行，
// 一次下滑约移动 180 px，4 页足够覆盖 4 行商品；用上限兜住「滑到底仍能滑动」
// 的极端情况，避免扫描循环无法结束。
const maxShelfPages = 4

func runShelfSwipe(ctx *maa.Context, nodeName string) error {
	if ctx == nil {
		return fmt.Errorf("context is nil")
	}

	_, err := ctx.RunAction(nodeName, maa.Rect{0, 0, 0, 0}, "", nil)
	return err
}

func swipeShelfDown(ctx *maa.Context) error {
	return runShelfSwipe(ctx, swipeShelfDownNodeName)
}

func swipeShelfUp(ctx *maa.Context) error {
	return runShelfSwipe(ctx, swipeShelfUpNodeName)
}

func screencapShelf(ctx *maa.Context) (image.Image, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is nil")
	}
	tasker := ctx.GetTasker()
	if tasker == nil {
		return nil, fmt.Errorf("tasker is nil")
	}
	ctrl := tasker.GetController()
	if ctrl == nil {
		return nil, fmt.Errorf("controller is nil")
	}

	ctrl.PostScreencap().Wait()
	img, err := ctrl.CacheImage()
	if err != nil {
		return nil, err
	}
	return img, nil
}

// isSelectedGoodsVisibleOnScreen 用点击节点当前的 ROI 与目标商品模板，在最新一帧
// 画面上确认该商品是否已经可以点击。扫描首屏可能截在货架展开动画里，把实际可见的
// 第二行商品记成「非首屏」；此时不该再滑动（ADB 上多余滑动会被吞，点击会停在旧画面）。
func isSelectedGoodsVisibleOnScreen(ctx *maa.Context, productID string) (bool, error) {
	if ctx == nil {
		return false, fmt.Errorf("context is nil")
	}

	node, err := ctx.GetNode(selectedGoodsClickNodeName)
	if err != nil {
		return false, err
	}
	roi, err := recognitionParamROI(node)
	if err != nil {
		return false, err
	}

	img, err := screencapShelf(ctx)
	if err != nil {
		return false, err
	}

	detail, err := runGoodsTemplateMatch(ctx, img, buildTemplatePath(productID), roi)
	if err != nil {
		return false, err
	}

	return detail != nil && detail.Hit, nil
}

// revealMaxExtraSwipes 是点击前补滑的上限。一次下滑在 ADB 上只移动约 200 px，
// 小于一行卡片的高度（约 267 px），按页号滑 N 次后目标仍可能停在货架底部被裁切
// 的位置：模板只覆盖图标上方约 2/3，匹配分数停在 0.7 左右，低于 0.8 阈值，
// 点击节点会一直重识别到 20 s 超时。
const revealMaxExtraSwipes = 2

// revealSelectedGoodsBeforeClick 把目标商品滑到可点击位置再交给点击节点。
// 已可见时直接返回；不可见时先按扫描得到的页号下滑，再逐次校验可见性并补滑。
// 返回 false 表示补滑后仍然不可见，调用方应跳过本分支而不是等点击节点超时；
// 可见性无法确认（截图或识别失败）时返回 true，保持原来的点击尝试行为。
func revealSelectedGoodsBeforeClick(ctx *maa.Context, productID string, swipes int) (bool, error) {
	visible, visibleErr := isSelectedGoodsVisibleOnScreen(ctx, productID)
	if visibleErr != nil {
		log.Warn().
			Err(visibleErr).
			Str("component", autoStockpileComponent).
			Str("product_id", productID).
			Str("step", "check_selected_goods_visible").
			Msg("failed to check whether selected goods is on screen, fall back to shelf swipe")
	}

	if visible {
		log.Info().
			Str("component", autoStockpileComponent).
			Str("product_id", productID).
			Int("swipes", swipes).
			Msg("selected goods is already on screen, skip shelf swipe before click")
		return true, nil
	}

	log.Info().
		Str("component", autoStockpileComponent).
		Str("product_id", productID).
		Int("swipes", swipes).
		Msg("selected goods is below the first screen, swipe shelf down before click")
	for i := 0; i < swipes; i++ {
		if err := swipeShelfDown(ctx); err != nil {
			log.Error().
				Err(err).
				Str("component", autoStockpileComponent).
				Str("product_id", productID).
				Str("step", "shelf_swipe_down_before_click").
				Int("attempt", i+1).
				Int("swipes", swipes).
				Msg("failed to reveal off-screen goods before click")
			return false, fmt.Errorf("swipe shelf down before click: %w", err)
		}
	}

	for extra := 0; extra <= revealMaxExtraSwipes; extra++ {
		if extra > 0 {
			if err := swipeShelfDown(ctx); err != nil {
				log.Error().
					Err(err).
					Str("component", autoStockpileComponent).
					Str("product_id", productID).
					Str("step", "shelf_swipe_down_before_click").
					Int("attempt", swipes+extra).
					Int("swipes", swipes).
					Msg("failed to reveal off-screen goods before click")
				return false, fmt.Errorf("swipe shelf down to reveal selected goods: %w", err)
			}
		}

		visible, visibleErr = isSelectedGoodsVisibleOnScreen(ctx, productID)
		if visibleErr != nil {
			log.Warn().
				Err(visibleErr).
				Str("component", autoStockpileComponent).
				Str("product_id", productID).
				Str("step", "check_selected_goods_visible").
				Int("extra_swipes", extra).
				Msg("failed to check selected goods visibility after swipe, keep click attempt")
			return true, nil
		}
		if visible {
			if extra > 0 {
				log.Info().
					Str("component", autoStockpileComponent).
					Str("product_id", productID).
					Int("extra_swipes", extra).
					Msg("selected goods revealed by extra shelf swipes")
			}
			return true, nil
		}

		log.Warn().
			Str("component", autoStockpileComponent).
			Str("product_id", productID).
			Str("step", "check_selected_goods_visible").
			Int("extra_swipes", extra).
			Msg("selected goods is still not on screen after swipe")
	}

	return false, nil
}

// restoreShelf 按扫描时下滑的次数滑回首屏；失败只记录警告，仍使用已扫到的结果。
func restoreShelf(ctx *maa.Context, swipes int) {
	for i := 0; i < swipes; i++ {
		if err := swipeShelfUp(ctx); err != nil {
			log.Warn().
				Err(err).
				Str("component", autoStockpileComponent).
				Str("step", "shelf_swipe_up").
				Int("attempt", i+1).
				Int("swipes", swipes).
				Msg("failed to restore shelf to the first screen")
			return
		}
	}
}

// scanGoodsWithPages 先扫描首屏，再逐屏下滑扫描，直到某一屏没有新增商品 ID 或达到
// maxShelfPages；按 ID 合并后返回商品列表与「不在首屏」商品的最早页号。
// 扫描结束后按相同下滑次数滑回首屏。
func scanGoodsWithPages(
	ctx *maa.Context,
	firstImg image.Image,
	region string,
	itemMap *ItemMap,
) (goods []GoodsItem, shelfPageByID map[string]int, abortReason AbortReason, err error) {
	page0, abortReason, err := scanGoodsOnImage(ctx, firstImg, region, itemMap)
	if abortReason != AbortReasonNone || err != nil {
		return nil, nil, abortReason, err
	}

	pages := [][]GoodsItem{page0}
	seen := make(map[string]struct{}, len(page0))
	for _, item := range page0 {
		seen[item.ID] = struct{}{}
	}

	swipesDown := 0
	restored := false
	defer func() {
		if restored || swipesDown == 0 {
			return
		}
		restoreShelf(ctx, swipesDown)
	}()

	for page := 1; page < maxShelfPages; page++ {
		if swipeErr := swipeShelfDown(ctx); swipeErr != nil {
			log.Warn().
				Err(swipeErr).
				Str("component", autoStockpileComponent).
				Str("step", "shelf_swipe_down").
				Int("page", page).
				Int("swipes_down", swipesDown).
				Msg("shelf swipe down failed, stop scanning further pages")
			break
		}
		swipesDown++

		shelfImg, capErr := screencapShelf(ctx)
		if capErr != nil {
			log.Warn().
				Err(capErr).
				Str("component", autoStockpileComponent).
				Str("step", "shelf_screencap").
				Int("page", page).
				Int("swipes_down", swipesDown).
				Msg("shelf screencap failed, stop scanning further pages")
			break
		}

		pageGoods, pageAbort, pageErr := scanGoodsOnImage(ctx, shelfImg, region, itemMap)
		if pageAbort != AbortReasonNone || pageErr != nil {
			log.Warn().
				Err(pageErr).
				Str("component", autoStockpileComponent).
				Str("step", "scan_page").
				Str("abort_reason", string(pageAbort)).
				Int("page", page).
				Int("swipes_down", swipesDown).
				Msg("shelf page scan failed, stop scanning further pages")
			break
		}

		newIDs := 0
		for _, item := range pageGoods {
			if _, ok := seen[item.ID]; ok {
				continue
			}
			seen[item.ID] = struct{}{}
			newIDs++
		}
		pages = append(pages, pageGoods)

		log.Info().
			Str("component", autoStockpileComponent).
			Str("step", "shelf_page_scanned").
			Int("page", page).
			Int("page_count", len(pageGoods)).
			Int("new_count", newIDs).
			Int("swipes_down", swipesDown).
			Msg("shelf page scanned")

		if newIDs == 0 {
			log.Info().
				Str("component", autoStockpileComponent).
				Str("step", "shelf_scan_stop").
				Int("page", page).
				Msg("shelf page adds no new goods, stop scanning")
			break
		}
	}

	goods, shelfPageByID = mergeGoodsByID(pages)

	restoreShelf(ctx, swipesDown)
	restored = true

	log.Info().
		Str("component", autoStockpileComponent).
		Str("step", "merge_pages").
		Int("pages_scanned", len(pages)).
		Int("merged_count", len(goods)).
		Int("shelf_page_count", len(shelfPageByID)).
		Int("swipes_down", swipesDown).
		Msg("multi-page goods scan merged")

	return goods, shelfPageByID, AbortReasonNone, nil
}
