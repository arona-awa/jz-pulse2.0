// 事件总线与过滤器链。
//
// 同时服务两种形态：
//   - A 型：http 钩子（Notify）与 html/filter 静态片段（CallHTML/CallFilter）
//   - B 型：clv.on / clv.slot / clv.filter / clv.middleware 注册的脚本回调
//
// 约定：插件故障不影响站点 —— 事件与中间件失败只记日志，过滤器失败放行原值。
package plugin

import (
	"context"

	"clearlove/internal/models"
)

func withMeta(event string, payload map[string]any) map[string]any {
	out := make(map[string]any, len(payload)+2)
	for k, v := range payload {
		out[k] = v
	}
	out["event"] = event
	out["time"] = models.Now()
	return out
}

// Emit 广播事件（A 型 Webhook + B 型 clv.on），B 型回调异步执行
func Emit(event string, payload map[string]any) {
	// A 型：现有 Webhook 行为保持不变
	Notify(event, payload)

	for _, h := range EventHooks(event) {
		h := h
		if h.App == nil || h.App.Worker == nil || h.App.Worker.Closed() {
			continue
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), h.App.Plugin.Timeout())
			defer cancel()
			_, err := h.App.Worker.Do(ctx, func(rt *jsRuntime) (any, error) {
				_, err := rt.callFn(h.Fn, withMeta(event, payload))
				return nil, err
			})
			if err != nil {
				noteFailure(h.App, err)
				return
			}
			noteSuccess(h.App)
		}()
	}
}

// ApplyFilterStr 字符串过滤器（filter.content / filter.nickname 等）。
// filter.content 会先执行 A 型声明式正则改写，再走 B 型运行时过滤器。
func ApplyFilterStr(name, value string) string {
	v := value
	if name == "filter.content" {
		v = CallFilter(v)
	}
	if len(FilterHooks(name)) == 0 {
		return v
	}
	out := runFilterChain(name, v, nil)
	if s, ok := out.(string); ok {
		return s
	}
	return v
}

// ApplyFilterValue 通用过滤器（map / 布尔 等）
func ApplyFilterValue(name string, value any, extra map[string]any) any {
	if len(FilterHooks(name)) == 0 {
		return value
	}
	return runFilterChain(name, value, extra)
}

// ApplyFilterBool 布尔过滤器（带上下文字段，如 filter.post.visible）
func ApplyFilterBool(name string, value bool, extra map[string]any) bool {
	if len(FilterHooks(name)) == 0 {
		return value
	}
	out := runFilterChain(name, value, extra)
	if b, ok := out.(bool); ok {
		return b
	}
	return value
}

// FilterVeto 布尔型过滤器：任一插件返回 false 即否决
func FilterVeto(name string, allow bool) bool {
	for _, h := range FilterHooks(name) {
		if h.App == nil || h.App.Worker == nil || h.App.Worker.Closed() {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), h.App.Plugin.Timeout())
		v, err := h.App.Worker.Do(ctx, func(rt *jsRuntime) (any, error) {
			return rt.callFn(h.Fn, allow)
		})
		cancel()
		if err != nil {
			noteFailure(h.App, err)
			continue
		}
		noteSuccess(h.App)
		switch t := v.(type) {
		case bool:
			if !t {
				return false
			}
		case nil:
			// 未表态
		default:
			if !boolOf(t) {
				return false
			}
		}
	}
	return allow
}

func runFilterChain(name string, value any, extra map[string]any) any {
	cur := value
	for _, h := range FilterHooks(name) {
		if h.App == nil || h.App.Worker == nil || h.App.Worker.Closed() {
			continue
		}
		h := h
		ctx, cancel := context.WithTimeout(context.Background(), h.App.Plugin.Timeout())
		out, err := h.App.Worker.Do(ctx, func(rt *jsRuntime) (any, error) {
			if extra == nil {
				return rt.callFn(h.Fn, cur)
			}
			return rt.callFn(h.Fn, cur, extra)
		})
		cancel()
		if err != nil {
			noteFailure(h.App, err)
			continue // 放行原值，不因插件故障阻断站点
		}
		noteSuccess(h.App)
		if out == nil {
			continue // 返回 undefined 表示不改
		}
		cur = out
	}
	return cur
}

// SlotHTML 计算某个 UI 注入点的完整 HTML（A 型静态片段 + B 型动态渲染）
func SlotHTML(name string, data any) string {
	out := CallHTML(name)
	for _, h := range SlotHooks(name) {
		h := h
		if h.App == nil || h.App.Worker == nil || h.App.Worker.Closed() {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), h.App.Plugin.Timeout())
		v, err := h.App.Worker.Do(ctx, func(rt *jsRuntime) (any, error) {
			return rt.callFn(h.Fn, data)
		})
		cancel()
		if err != nil {
			noteFailure(h.App, err)
			continue
		}
		noteSuccess(h.App)
		if v == nil {
			continue
		}
		out += strOf(v)
	}
	return out
}

// RunMiddlewareAfter 执行 http.after 中间件（只观察，忽略返回值与异常细节）
func RunMiddlewareAfter(req map[string]any) {
	for _, h := range MiddlewareHooks("http.after") {
		if h.App == nil || h.App.Worker == nil || h.App.Worker.Closed() {
			continue
		}
		h := h
		ctx, cancel := context.WithTimeout(context.Background(), h.App.Plugin.Timeout())
		_, err := h.App.Worker.Do(ctx, func(rt *jsRuntime) (any, error) {
			rt.cur = nil
			return rt.callFn(h.Fn, req)
		})
		cancel()
		if err != nil {
			noteFailure(h.App, err)
			continue
		}
		noteSuccess(h.App)
	}
}

// MiddlewareResult http.before 中间件的短路指令
type MiddlewareResult struct {
	Abort   bool
	Status  int
	Headers map[string]string
	Body    string
	JSON    any
	Redirect string
}

// RunMiddlewareBefore 执行 http.before 中间件；返回 nil 表示继续正常处理
func RunMiddlewareBefore(req map[string]any) *MiddlewareResult {
	for _, h := range MiddlewareHooks("http.before") {
		if h.App == nil || h.App.Worker == nil || h.App.Worker.Closed() {
			continue
		}
		h := h
		ctx, cancel := context.WithTimeout(context.Background(), h.App.Plugin.Timeout())
		v, err := h.App.Worker.Do(ctx, func(rt *jsRuntime) (any, error) {
			return rt.callFn(h.Fn, req)
		})
		cancel()
		if err != nil {
			noteFailure(h.App, err)
			continue
		}
		noteSuccess(h.App)
		m, ok := v.(map[string]any)
		if !ok || !boolOf(m["abort"]) {
			continue
		}
		res := &MiddlewareResult{
			Abort:   true,
			Status:  intOf(m["status"]),
			Body:    strOf(m["body"]),
			JSON:    m["json"],
			Redirect: strOf(m["redirect"]),
		}
		if res.Status == 0 {
			res.Status = 200
		}
		if hdrs, ok := m["headers"].(map[string]any); ok {
			res.Headers = make(map[string]string, len(hdrs))
			for k, hv := range hdrs {
				res.Headers[k] = strOf(hv)
			}
		}
		return res
	}
	return nil
}
