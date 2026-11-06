# 开发手册 - AutoStockpile 维护文档

本文说明 `AutoStockpile` 如何在版本更新时进行维护

## 概念说明

### 商品 ID

`item_map.json` 中保存的不是图片路径，而是**内部商品 ID**，格式固定为：

```text
{Region}/{BaseName}.Tier{N}
```

例如：

```text
ValleyIV/OriginiumSaplings.Tier3
Wuling/WulingFrozenPears.Tier1
```

其中：

1. `Region`：地区 ID。
2. `BaseName`：英文文件名主体。可在[EndFieldTranslationReferrer](https://susieglitter.github.io/EndFieldTranslationReferrer/)中找到对应名称
3. `Tier{N}`：价值变动幅度。

### 模板图片路径

Go 代码会根据商品 ID 自动拼出模板路径：

```text
AutoStockpile/Goods/{Region}/{BaseName}.Tier{N}.png
```

仓库中的实际文件位置为：

```text
assets/resource/image/AutoStockpile/Goods/{Region}/{BaseName}.Tier{N}.png
```

### 地区与价格选项

当前仓库内已使用的地区与档位：

| 中文名 | Region ID |
| -------- | ---------- |
| 四号谷地 | `ValleyIV` |
| 武陵 | `Wuling` |

| 档位 | 价格波动幅度 |
| ------- | ------------ |
| `Tier1` | 适中 |
| `Tier2` | 较大 |
| `Tier3` | 极大 |

价格阈值只由 `strategy.go` 的「地区基准 + 档位基准 + 星期调整」公式产出，已无 JSON 配置入口；需要调整阈值时只能修改该公式。

## 添加商品

添加新商品时，需要维护**商品映射**和**模板图片**两部分。

### 1. 修改 `item_map.json`

文件：`agent/go-service/autostockpile/item_map.json`

在 `zh_cn` 下新增商品名称到商品 ID 的映射：

```json
{
    "zh_cn": {
        "{商品中文名}": "{Region}/{BaseName}.Tier{N}"
    }
}
```

### 2. 添加模板图片

在弹性需求物资的商品列表里截取该商品的图片，保存到对应目录：

```text
assets/resource/image/AutoStockpile/Goods/{Region}/{BaseName}.Tier{N}.png
```

> [!important]
> 模板图片需基于1280×720分辨率

## 添加地区

新增地区需要同步修改以下文件：

### 1. 准备资源

- 建立 `assets/resource/image/AutoStockpile/Goods/{NewRegion}/` 目录并放入模板图片。
- 在 `agent/go-service/autostockpile/item_map.json` 中加入新地区的商品名称到商品 ID 的映射。

### 2. 配置任务选项

文件：`assets/tasks/AutoStockpile.json`

在 `option` 列表中新增 `AutoStockpileElastic{NewRegion}` 开关项，通过 `pipeline_override.enabled` 控制 `Main.json` 中对应地区节点是否启用。模板如下：

```json
"AutoStockpileElastic{NewRegion}": {
    "type": "switch",
    "label": "$task.AutoStockpile.option.AutoStockpileElastic{NewRegion}.label",
    "default_case": "Yes",
    "cases": [
        {
            "name": "Yes",
            "pipeline_override": {
                "AutoStockpileElastic{NewRegion}": {
                    "enabled": true
                }
            }
        },
        {
            "name": "No"
        }
    ]
}
```

同时在 `task[0].option` 数组中加入 `"AutoStockpileElastic{NewRegion}"`。

### 3. 确认场景导航入口存在

通常被命名为 `SceneEnterMenuRegionalDevelopment{Region}StockRedistribution`。

如果不存在，可等待其他维护者提供相应节点，或者自行补充，此处不再赘述。

### 4. Pipeline 节点

#### Main.json

文件：`assets/resource/pipeline/AutoStockpile/Main.json`

1. 在 `AutoStockpileStart` 的 `next` 列表中加入 `"[JumpBack]AutoStockpileElastic{NewRegion}"`。
2. 新增 `AutoStockpileElastic{NewRegion}` 节点，参照 `AutoStockpileElasticValleyIV` 的结构：
    - `action.param.custom_action_param.sub` 中填入对应的场景导航子任务名称。
    - `anchor` 设置 `"AutoStockpileDecision": "AutoStockpileDecision{NewRegion}"`。
    - `focus` 中填入地区中文名。

#### DecisionLoop.json

文件：`assets/resource/pipeline/AutoStockpile/DecisionLoop.json`

新增 `AutoStockpileDecision{NewRegion}` 节点，在 `action.param.custom_action_param` 中写入 `"Region": "{NewRegion}"`；若新地区详情页的现金图标与现有的不同，还要把新模板加入 `AutoStockpileEnsureCashAnchor` 的 `template`。

### 5. Go 逻辑

文件：`agent/go-service/autostockpile/strategy.go`

在 `regionBases` 中补充新地区及其基础值：

```go
var regionBases = map[string]int{
    "ValleyIV":   regionBaseValleyIV,
    "Wuling":     regionBaseWuling,
    "{NewRegion}": regionBase{NewRegion},  // 新增
}
```

确认共享的 `tierBases` 已覆盖该地区需要支持的档位。

### 6. 国际化

在所有语言的 `assets/locales/interface/*.json` 中补充以下字段：

| 字段键 | 用途 |
| ----------------------------------------------------------------- | -------------- |
| `global.region.{NewRegion}` | 地区全局显示名 |
| `task.AutoStockpile.option.AutoStockpileElastic{NewRegion}.label` | 任务选项标签 |

需补充的语言文件：`zh_cn.json`、`en_us.json`、`ja_jp.json`、`ko_kr.json`、`zh_tw.json`。

### 7. 更新价格记录 Schema

文件：`docs/zh_cn/protocol/autostockpile-daily-storage/daily_storage.schema.json`（以及对应 `en_us` 版本）

在 `region` 字段的 `enum` 列表中加入新地区标识（如 `"NewRegion"`），确保第三方工具能通过 Schema 校验新增地区的数据。

## 货架多页扫描

识别阶段会对弹性货架做**逐屏扫描**（含首屏最多 `maxShelfPages = 4` 屏），避免清单超出一屏时漏扫商品：

1. 对首屏截图执行 OCR + 模板匹配，记为 page0。
2. 调用 `AutoStockpileSwipeShelfDown` 下滑一次（`post_wait_freezes` 等待列表稳定）。
3. 再截图扫描，记为 page1；若本屏出现首屏未见过的商品 ID，继续下滑扫描 page2、page3……
4. 某一屏没有新增商品 ID（已滑到底或页面不再变化）或达到 4 屏上限时停止。
5. 按商品 **ID 去重合并**（同 ID 保留最早出现的那一屏结果），并把「首屏没有的商品 ID → 其最早出现的 0-based 页号」记入 `ShelfPageByID`；页号即点击前需要下滑的次数。
6. 按扫描时实际下滑的次数，逐次调用 `AutoStockpileSwipeShelfUp` 滑回首屏。

`SecondPageOnlyIDs` 仍会输出，但语义收窄为 `ShelfPageByID` 中页号等于 1 的子集（仅下滑一次后可见），用于兼容旧消费方与日志；判定点击前滑几次以下滑次数字段 `ShelfPageByID` 为准。

下滑、截图或某一屏扫描失败时，用**已扫到的页**继续合并，不中断任务；滑回首屏失败只记录警告，仍使用合并结果。

选中商品后：

- 若商品在 `ShelfPageByID` 中且页号 `N > 0`（首屏不可见，例如第二、三排），点击前会连续执行 `N` 次 `AutoStockpileSwipeShelfDown`，再交给 `AutoStockpileSelectedGoodsClick` 做模板点击。
- 若首屏（page0）已有该商品，则不额外滑动。
- 旧数据只有 `SecondPageOnlyIDs` 而没有 `ShelfPageByID` 时，按下滑 1 次回退处理。

滑动节点：

- Win32 默认：`assets/resource/pipeline/AutoStockpile/Helper.json` 中为 `DoNothing`（一屏可看全）。
- ADB / PlayCover：`assets/resource_adb/pipeline/AutoStockpile/Helper.json` 覆盖为真实 `Swipe`（含横向收尾与 `end_hold`）。坐标基于 720p，实机若漏扫或过量重复，优先微调 ADB 侧 `begin` / `end` 与 `post_wait_freezes.target`。

## 价格识别与折扣行过滤

价格与商品名来自同一次 OCR（`AutoStockpileGetGoods` 节点），价格候选只接受 `^\d{3,4}$` 的纯数字文本。

货架上的折扣标签（如 `13.6%`）位于价格行上方约 25~30 px。OCR 在低分辨率或文字粘连时会把它整行读成 `1364` / `136` 这类纯数字，从而混入价格候选并抢占真实价格的绑定。为此：

1. 先收集本屏所有含 `%`（或全角 `％`）的 OCR 文本的中心 Y。
2. 若某个纯数字候选的中心 Y 与任一折扣文本的中心 Y 相差不超过 `discountRowToleranceY = 10` px，则视为折扣行的误读并丢弃（日志 `step=skip_discount_price`）。
3. 折扣标签本身在价格上方约 25~30 px，不会误伤同一列的真实价格；同一行内其他列的百分比文本也一并用于判定。

若某屏的折扣文本全部被 OCR 丢掉 `%`，该屏的折扣行仍可能进入价格候选；此时绑定会按纵向距离选最近的商品，通常仍落在正确的一行。
