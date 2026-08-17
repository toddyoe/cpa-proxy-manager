package main

// i18n.go centralizes operator-visible backend strings. The plugin ships a
// single Chinese pack per the design decision; keys keep the structure ready
// for additional languages without reworking call sites.
import "fmt"

var messages = map[string]string{
	"err_invalid_json":   "请求体不是合法的 JSON",
	"err_invalid_mode":   "执行模式必须是 set 或 clear",
	"err_busy":           "已有批次在执行中，请等待完成或先停止",
	"err_no_targets":     "没有符合条件的执行目标",
	"err_host_unable":    "宿主回调不可用，无法读取凭据",
	"err_unknown_target": "以下账号已不存在：",
	"saved":              "配置已保存",
}

// T returns the message for key, falling back to the key itself.
func T(key string) string {
	if v, ok := messages[key]; ok {
		return v
	}
	return key
}

// Tf formats a message with arguments.
func Tf(key string, args ...any) string {
	return fmt.Sprintf(T(key), args...)
}
