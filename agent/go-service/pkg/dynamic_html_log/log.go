package dynamic_html_log

import (
	"strconv"
	"sync"
	"time"

	"github.com/MaaXYZ/MaaEnd/agent/go-service/pkg/i18n"
	"github.com/MaaXYZ/MaaEnd/agent/go-service/pkg/maafocus"
	"github.com/MaaXYZ/MaaEnd/agent/go-service/pkg/pienv"
)

const (
	templateKey = "dynamic_html_log.log"
	defClass    = `text-accent`
)

var (
	idPrefix string
	logIndex = 0
	mu       sync.Mutex
)

func init() {
	// 初始化时生成一个id前缀，每个agent的id前缀独立
	idPrefix = time.Now().Format("20060102150405")
}

type DynamicHtmlLog struct {
	// 每个实例中的id一致且实例间隔离，用于实现旧日志隐藏
	// 将id写入到输出的html文本中，再使用css查询器隐藏非最后一条日志
	id string
	// 最后一次输出的日志，防止输出重复日志
	lastMessage string
	// plainText 表示当前客户端不能执行 CSS。倒计时在这里整段不输出：
	// 它每秒一条，去掉 HTML 后仍是日志噪声，MaaFwApp 也没有地方展示它。
	plainText bool
}

type DynamicHtmlLogData struct {
	log     *DynamicHtmlLog
	class   string
	style   string
	message string
}

// New 生成一个日志实例
func New() *DynamicHtmlLog {
	mu.Lock()
	defer mu.Unlock()
	logIndex += 1
	return &DynamicHtmlLog{
		id:        idPrefix + `-` + strconv.Itoa(logIndex), //唯一id
		plainText: pienv.PlainTextFocusClient(),
	}
}

func (log *DynamicHtmlLog) T(key string, args ...any) *DynamicHtmlLogData {
	return log.Message(i18n.T(key, args...)) //使用i18n转换key和参数为文本
}

func (log *DynamicHtmlLog) Message(message string) *DynamicHtmlLogData {
	return &DynamicHtmlLogData{
		log:     log,
		message: message,
	}
}

func (data *DynamicHtmlLogData) WithClass(class string) *DynamicHtmlLogData {
	// 给输出添加class，默认无class
	data.class = class
	return data
}

func (data *DynamicHtmlLogData) WithStyle(style string) *DynamicHtmlLogData {
	// 给输出添加style，默认无style
	// 添加style时注意适配暗色主题
	data.style = style
	return data
}

func (data *DynamicHtmlLogData) Print() {
	if !data.shouldPrint() {
		return
	}
	maafocus.PrintLargeContentTrimNewline(data.render())
	data.log.lastMessage = data.message
}

// shouldPrint suppresses the whole countdown on plain-text clients: it is
// per-second log noise there, and Markwon has no CSS engine to hide old entries.
func (data *DynamicHtmlLogData) shouldPrint() bool {
	return !data.log.plainText && data.log.lastMessage != data.message
}

// render builds the focus payload. Only used for clients whose renderer
// executes CSS (MXU); MaaFwApp never reaches here.
func (data *DynamicHtmlLogData) render() string {
	// 每条日志都携带隐藏样式，避免客户端裁切旧日志后丢失样式。
	if data.class == "" {
		data.class = defClass
	}
	return i18n.RenderHTML(templateKey, map[string]any{
		"id":      data.log.id,
		"class":   data.class,
		"style":   data.style,
		"message": data.message,
	})
}
