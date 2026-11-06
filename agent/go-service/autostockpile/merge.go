package autostockpile

// mergeGoodsByID 按商品 ID 合并多页扫描结果：同 ID 保留最早出现的条目，
// 并返回「不在首屏」的商品 ID → 其最早出现的 0-based 页号。
// 页号即点击该商品前需要下滑货架的次数。
func mergeGoodsByID(pages [][]GoodsItem) (goods []GoodsItem, shelfPageByID map[string]int) {
	total := 0
	for _, page := range pages {
		total += len(page)
	}

	seen := make(map[string]struct{}, total)
	goods = make([]GoodsItem, 0, total)
	shelfPageByID = make(map[string]int)

	for pageIndex, page := range pages {
		for _, item := range page {
			if _, ok := seen[item.ID]; ok {
				continue
			}
			seen[item.ID] = struct{}{}
			goods = append(goods, item)
			if pageIndex > 0 {
				shelfPageByID[item.ID] = pageIndex
			}
		}
	}

	return goods, shelfPageByID
}

// secondPageOnlyIDsFromPages 按商品顺序挑出页号为 1 的商品 ID：既兼容旧的
// SecondPageOnlyIDs 字段语义，也让日志顺序稳定（map 遍历本身无序）。
func secondPageOnlyIDsFromPages(goods []GoodsItem, shelfPageByID map[string]int) []string {
	ids := make([]string, 0, len(shelfPageByID))
	for _, item := range goods {
		if shelfPageByID[item.ID] == 1 {
			ids = append(ids, item.ID)
		}
	}
	return ids
}
