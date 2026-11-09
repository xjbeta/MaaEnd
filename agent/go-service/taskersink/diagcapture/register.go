package diagcapture

import (
	maa "github.com/MaaXYZ/maa-framework-go/v4"
	"github.com/rs/zerolog/log"
)

// Register 注册诊断留证 sink。
//
// 是否生效按控制器类型在事件里判断：只有安卓/移动端控制器才留证，
// 桌面端（Win32/macOS）直接跳过。
func Register() {
	maa.AgentServerAddContextSink(NewSink())
	log.Info().
		Str("component", component).
		Msg("diagnostic capture sink registered")
}
