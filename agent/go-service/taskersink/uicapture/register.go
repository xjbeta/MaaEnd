package uicapture

import (
	maa "github.com/MaaXYZ/maa-framework-go/v4"
	"github.com/rs/zerolog/log"
)

// Register 注册界面留证 sink。
//
// 是否生效完全由 files/debug/ui_capture.json 决定：文件不存在或 enabled 为
// false 时，事件回调只做一次 mtime 检查，不产生任何额外截图与磁盘写入。
func Register() {
	maa.AgentServerAddContextSink(NewSink())
	log.Info().
		Str("component", component).
		Msg("ui capture sink registered")
}
