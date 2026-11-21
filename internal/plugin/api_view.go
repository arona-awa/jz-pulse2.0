// Host API：运行时注册（clv.route / clv.slot / clv.on / clv.filter / clv.job ...）
// 与视图渲染（clv.view.*）。
//
// 这里体现 B 型插件的核心设计：能力注册是命令式的（写在 setup() 里），
// 而能力授权是声明式的（写在 plugin.json 的 permissions 里）。
package plugin

import (
	"errors"
	"fmt"
	"html/template"
	"os"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// reqContext 当前请求上下文。
// Worker 串行执行，同一时刻只有一个 handler 在跑，因此可以用单字段暂存。
type reqContext struct {
	UserID      int64
	AdminID     int64
	IsAdmin     bool
	IP          string
	Fingerprint string
	Method      string
	Path        string
	Params      map[string]string
	Query       map[string]string
	Form        map[string]string
	CSRF        string
}

func (rt *jsRuntime) installView(clv *goja.Object) error {
	// ---------- 注册类 API ----------
	_ = clv.Set("route", rt.jsFn(rt.regRoute))
	_ = clv.Set("adminMenu", rt.jsFn(rt.regAdminMenu))
	_ = clv.Set("adminPage", rt.jsFn(rt.regAdminPage))
	_ = clv.Set("slot", rt.jsFn(rt.regSlot))
	_ = clv.Set("on", rt.jsFn(rt.regEvent))
	_ = clv.Set("emit", rt.jsFn(rt.emitEvent))
	_ = clv.Set("filter", rt.jsFn(rt.regFilter))
	_ = clv.Set("job", rt.jsFn(rt.regJob))
	_ = clv.Set("middleware", rt.jsFn(rt.regMiddleware))

	// ---------- clv.view ----------
	v := rt.vm.NewObject()
	_ = v.Set("template", rt.jsFn(rt.viewTemplate))
	_ = v.Set("escape", rt.jsFn(func(call goja.FunctionCall) (any, error) {
		args := argsOf(call)
		if len(args) == 0 {
			return "", nil
		}
		return template.HTMLEscapeString(strOf(args[0])), nil
	}))
	_ = v.Set("nl2br", rt.jsFn(func(call goja.FunctionCall) (any, error) {
		args := argsOf(call)
		if len(args) == 0 {
			return "", nil
		}
		return strings.ReplaceAll(template.HTMLEscapeString(strOf(args[0])), "\n", "<br>"), nil
	}))
	_ = clv.Set("view", v)
	return nil
}

// ---------- 注册实现 ----------

// bindFn 支持两种写法：函数名字符串，或直接传函数（自动登记为内部名，便于跨请求调用）
func (rt *jsRuntime) bindFn(v goja.Value, prefix string) (string, error) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return "", errors.New("需要一个函数或函数名")
	}
	if fnVal, ok := goja.AssertFunction(v); ok {
		rt.fnSeq++
		name := fmt.Sprintf("__%s_%d", prefix, rt.fnSeq)
		if err := rt.vm.Set(name, fnVal); err != nil {
			return "", err
		}
		return name, nil
	}
	name := strings.TrimSpace(strOf(exportJS(v)))
	if name == "" {
		return "", errors.New("需要一个函数或函数名")
	}
	return name, nil
}

func (rt *jsRuntime) regRoute(call goja.FunctionCall) (any, error) {
	if rt.app == nil {
		return nil, errNoApp
	}
	if len(call.Arguments) < 3 {
		return nil, errors.New("clv.route(method, path, handler, opts?) 参数不足")
	}
	method := strings.ToUpper(strings.TrimSpace(strOf(exportJS(call.Argument(0)))))
	path := normRelPath(strOf(exportJS(call.Argument(1))))
	fn, err := rt.bindFn(call.Argument(2), "route")
	if err != nil {
		return nil, err
	}
	r := RouteReg{Method: method, Path: path, Fn: fn, Auth: "none"}
	if opts := mapOfAny(call.Argument(3)); opts != nil {
		if v := strings.ToLower(strOf(opts["auth"])); v != "" {
			r.Auth = v
		}
		r.Perm = strings.TrimSpace(strOf(opts["perm"]))
		r.JSON = boolOf(opts["json"])
	}
	if method == "" {
		r.Method = "GET"
	}
	rt.app.Routes = append(rt.app.Routes, r)
	return nil, nil
}

func (rt *jsRuntime) regAdminMenu(call goja.FunctionCall) (any, error) {
	if rt.app == nil {
		return nil, errNoApp
	}
	opts := mapOfAny(call.Argument(0))
	if opts == nil {
		return nil, errors.New("clv.adminMenu({label, path, perm, order}) 需要一个对象参数")
	}
	label := trimTo(strOf(opts["label"]), 24)
	if label == "" {
		return nil, errors.New("adminMenu 需要 label")
	}
	rt.app.Menus = append(rt.app.Menus, MenuReg{
		Label: label,
		Path:  normRelPath(strOf(opts["path"])),
		Perm:  strings.TrimSpace(strOf(opts["perm"])),
		Order: intOf(opts["order"]),
	})
	return nil, nil
}

func (rt *jsRuntime) regAdminPage(call goja.FunctionCall) (any, error) {
	if rt.app == nil {
		return nil, errNoApp
	}
	if len(call.Arguments) < 2 {
		return nil, errors.New("clv.adminPage(path, handler, opts?) 参数不足")
	}
	path := normRelPath(strOf(exportJS(call.Argument(0))))
	fn, err := rt.bindFn(call.Argument(1), "adminpage")
	if err != nil {
		return nil, err
	}
	p := PageReg{Path: path, Fn: fn}
	if opts := mapOfAny(call.Argument(2)); opts != nil {
		p.Perm = strings.TrimSpace(strOf(opts["perm"]))
	}
	rt.app.Pages = append(rt.app.Pages, p)
	return nil, nil
}

func (rt *jsRuntime) regSlot(call goja.FunctionCall) (any, error) {
	if rt.app == nil {
		return nil, errNoApp
	}
	if len(call.Arguments) < 2 {
		return nil, errors.New("clv.slot(name, handler) 参数不足")
	}
	name := strings.TrimSpace(strOf(exportJS(call.Argument(0))))
	if name == "" {
		return nil, errors.New("slot 名称不能为空")
	}
	fn, err := rt.bindFn(call.Argument(1), "slot")
	if err != nil {
		return nil, err
	}
	rt.app.Slots[name] = append(rt.app.Slots[name], fn)
	return nil, nil
}

func (rt *jsRuntime) regEvent(call goja.FunctionCall) (any, error) {
	if rt.app == nil {
		return nil, errNoApp
	}
	if len(call.Arguments) < 2 {
		return nil, errors.New("clv.on(event, handler) 参数不足")
	}
	event := strings.TrimSpace(strOf(exportJS(call.Argument(0))))
	if event == "" {
		return nil, errors.New("事件名不能为空")
	}
	fn, err := rt.bindFn(call.Argument(1), "event")
	if err != nil {
		return nil, err
	}
	rt.app.Events[event] = append(rt.app.Events[event], fn)
	return nil, nil
}

// emitEvent 广播自定义事件（插件间通信）；订阅方在自己的 worker 中异步执行，不会死锁
func (rt *jsRuntime) emitEvent(call goja.FunctionCall) (any, error) {
	args := argsOf(call)
	if len(args) == 0 || strOf(args[0]) == "" {
		return nil, errors.New("clv.emit(event, payload?) 需要事件名")
	}
	payload := map[string]any{}
	if len(args) > 1 {
		if m, ok := args[1].(map[string]any); ok {
			payload = m
		}
	}
	Emit(strOf(args[0]), payload)
	return nil, nil
}

func (rt *jsRuntime) regFilter(call goja.FunctionCall) (any, error) {
	if rt.app == nil {
		return nil, errNoApp
	}
	if len(call.Arguments) < 2 {
		return nil, errors.New("clv.filter(name, handler) 或 clv.filter(name, priority, handler) 参数不足")
	}
	name := strings.TrimSpace(strOf(exportJS(call.Argument(0))))
	if name == "" {
		return nil, errors.New("过滤器名不能为空")
	}
	priority := 100
	fnArg := call.Argument(1)
	if len(call.Arguments) >= 3 {
		priority = intOf(exportJS(call.Argument(1)))
		fnArg = call.Argument(2)
	} else if _, ok := goja.AssertFunction(call.Argument(1)); !ok {
		// 只给了一个非函数参数：视为优先级缺失，报错更友好
		return nil, errors.New("clv.filter(name, priority, handler) 的 handler 必须是函数")
	}
	fn, err := rt.bindFn(fnArg, "filter")
	if err != nil {
		return nil, err
	}
	rt.app.Filters[name] = append(rt.app.Filters[name], FilterReg{Fn: fn, Priority: priority})
	return nil, nil
}

func (rt *jsRuntime) regJob(call goja.FunctionCall) (any, error) {
	if rt.app == nil {
		return nil, errNoApp
	}
	if len(call.Arguments) < 3 {
		return nil, errors.New("clv.job(name, every, handler) 参数不足")
	}
	name := trimTo(strOf(exportJS(call.Argument(0))), 40)
	every, err := parseEvery(strOf(exportJS(call.Argument(1))))
	if err != nil {
		return nil, err
	}
	fn, err := rt.bindFn(call.Argument(2), "job")
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = fn
	}
	rt.app.Jobs = append(rt.app.Jobs, JobReg{Name: name, Every: every, Fn: fn})
	return nil, nil
}

func (rt *jsRuntime) regMiddleware(call goja.FunctionCall) (any, error) {
	if rt.app == nil {
		return nil, errNoApp
	}
	if len(call.Arguments) < 2 {
		return nil, errors.New("clv.middleware(phase, handler) 参数不足")
	}
	phase := strings.ToLower(strings.TrimSpace(strOf(exportJS(call.Argument(0)))))
	if phase != "http.before" && phase != "http.after" {
		return nil, errors.New("middleware phase 仅支持 \"http.before\" 或 \"http.after\"")
	}
	fn, err := rt.bindFn(call.Argument(1), "mw")
	if err != nil {
		return nil, err
	}
	rt.app.Middlewares = append(rt.app.Middlewares, MiddlewareReg{Phase: phase, Fn: fn})
	return nil, nil
}

// ---------- 视图 ----------

func (rt *jsRuntime) viewTemplate(call goja.FunctionCall) (any, error) {
	if viewProvider == nil {
		return nil, errors.New("当前环境未装配模板渲染能力")
	}
	args := argsOf(call)
	if len(args) == 0 {
		return nil, errors.New("clv.view.template(name, data?) 需要模板名")
	}
	name := strings.TrimSpace(strOf(args[0]))
	if name == "" || strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
		return nil, errors.New("非法的模板名")
	}
	var data any = map[string]any{}
	if len(args) > 1 {
		data = args[1]
	}
	return viewProvider.RenderTemplate(os.DirFS(rt.dir), name, data)
}

// ---------- 工具 ----------

// normRelPath 归一化相对路径（保证以 / 开头、无结尾斜杠）
func normRelPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	p = strings.ReplaceAll(p, "\\", "/")
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if strings.Contains(p, "..") {
		return "/"
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
		if p == "" {
			return "/"
		}
	}
	return p
}

// parseEvery 解析任务周期，最小 10 秒，避免插件写 1ms 打爆 CPU
func parseEvery(s string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("无法解析周期 %q（示例：30s / 10m / 1h / 24h）", s)
	}
	if d < 10*time.Second {
		return 0, errors.New("任务周期不能小于 10s")
	}
	return d, nil
}

// trimTo 截断到 n 个字符（按 rune，避免截断中文）
func trimTo(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
