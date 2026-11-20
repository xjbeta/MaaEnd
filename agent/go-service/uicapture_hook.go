package main

import (
	"github.com/MaaXYZ/MaaEnd/agent/go-service/taskersink/uicapture"
)

// 界面留证是独立可选能力：实现、配置读取、落盘全部在 taskersink/uicapture 包里，
// 与上游唯一的接线点就是 registerAll() 里的 registerUICapture() 一行，
// 不新增 Pipeline 节点、不改 Schema、不动日志/截图目录约定，
// 上游改动 registerAll() 的其它位置时不会与这里冲突。
func registerUICapture() {
	uicapture.Register()
}
