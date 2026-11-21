// goja 引擎实现：脚本加载、函数调用、Host API 装配。
package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dop251/goja"

	"clearlove/internal/config"
	"clearlove/internal/models"
	"clearlove/internal/util"
)

// jsRuntime 单个插件的脚本运行时（只允许通过 Worker 访问）
type jsRuntime struct {
	vm     *goja.Runtime
	dir    string
	plugin *Plugin
	app    *App // 注册阶段回填，Host API 用它做权限校验

	cur   *reqContext // 当前请求上下文（worker 串行，同一时刻仅一个）
	fnSeq int         // 内联函数自增序号，用于生成 __slot_1 之类的内部函数名
}

// newJSRuntime 创建运行时并装配 Host API
func newJSRuntime(dir string, p *Plugin) (*jsRuntime, error) {
	vm := goja.New()
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	rt := &jsRuntime{vm: vm, dir: dir, plugin: p}
	if err := rt.installAPI(); err != nil {
		return nil, err
	}
	return rt, nil
}

// Stop 中断当前执行（超时保护）
func (rt *jsRuntime) Stop() {
	if rt == nil || rt.vm == nil {
		return
	}
	defer func() { _ = recover() }()
	rt.vm.Interrupt("timeout")
}

// loadEntry 读取并执行入口脚本（顶层代码只做定义，不应有副作用）
func (rt *jsRuntime) loadEntry() error {
	entry := rt.plugin.entryPath()
	if entry == "" {
		return errors.New("runtime.entry 缺失或非法（不允许绝对路径与 .. 穿越）")
	}
	full := filepath.Join(rt.dir, filepath.FromSlash(entry))
	data, err := os.ReadFile(full)
	if err != nil {
		return fmt.Errorf("读取入口脚本失败: %w", err)
	}
	if _, err := rt.vm.RunScript(entry, string(data)); err != nil {
		return fmt.Errorf("执行入口脚本失败: %w", err)
	}
	return nil
}

// hasFn 脚本是否定义了某函数
func (rt *jsRuntime) hasFn(name string) bool {
	_, ok := goja.AssertFunction(rt.vm.Get(name))
	return ok
}

// callFn 调用脚本函数，参数与返回值均为可 JSON 化的 Go 值
func (rt *jsRuntime) callFn(fn string, args ...any) (any, error) {
	f, ok := goja.AssertFunction(rt.vm.Get(fn))
	if !ok {
		return nil, fmt.Errorf("插件未定义函数 %s", fn)
	}
	vals := make([]goja.Value, 0, len(args))
	for _, a := range args {
		vals = append(vals, rt.vm.ToValue(a))
	}
	v, err := f(goja.Undefined(), vals...)
	if err != nil {
		return nil, err
	}
	return exportJS(v), nil
}

// ---------- 值转换 ----------

// exportJS goja 值 -> 纯 Go 值
func exportJS(v goja.Value) any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	return v.Export()
}

// argsOf 导出全部实参
func argsOf(call goja.FunctionCall) []any {
	out := make([]any, 0, len(call.Arguments))
	for _, a := range call.Arguments {
		out = append(out, exportJS(a))
	}
	return out
}

// objOf 导出对象参数字典
func objOf(v goja.Value) map[string]any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	if m, ok := v.Export().(map[string]any); ok {
		return m
	}
	return nil
}

func strOf(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "1"
		}
		return "0"
	default:
		return fmt.Sprint(t)
	}
}

func intOf(v any) int {
	switch t := v.(type) {
	case int64:
		return int(t)
	case int:
		return t
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	default:
		return 0
	}
}

func boolOf(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "1" || strings.EqualFold(t, "true")
	case int64:
		return t != 0
	case float64:
		return t != 0
	default:
		return false
	}
}

// jsFn 包装 Go 函数为 goja 函数：错误以 JS 异常抛出（插件可 try/catch）
func (rt *jsRuntime) jsFn(fn func(call goja.FunctionCall) (any, error)) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		v, err := fn(call)
		if err != nil {
			panic(rt.vm.NewGoError(err))
		}
		if v == nil {
			return goja.Undefined()
		}
		return rt.vm.ToValue(v)
	}
}

// ---------- Host API 装配 ----------

func (rt *jsRuntime) installAPI() error {
	clv := rt.vm.NewObject()
	if err := rt.installCore(clv); err != nil {
		return err
	}
	if err := rt.installDB(clv); err != nil {
		return err
	}
	if err := rt.installSite(clv); err != nil {
		return err
	}
	if err := rt.installView(clv); err != nil {
		return err
	}
	if err := rt.installNet(clv); err != nil {
		return err
	}
	rt.vm.Set("clv", clv)
	return nil
}

// installCore clv 的基础部分：元信息、日志、JSON、配置
func (rt *jsRuntime) installCore(clv *goja.Object) error {
	p := rt.plugin

	// clv.version
	if err := clv.Set("version", AppVersion); err != nil {
		return err
	}

	// clv.plugin.{name,slug,dir,version,config,configs}
	pl := rt.vm.NewObject()
	_ = pl.Set("name", p.Name)
	_ = pl.Set("slug", p.SlugOf())
	_ = pl.Set("dir", rt.dir)
	_ = pl.Set("version", p.Version)
	_ = pl.Set("config", rt.jsFn(func(call goja.FunctionCall) (any, error) {
		args := argsOf(call)
		if len(args) == 0 {
			return nil, errors.New("clv.plugin.config(key) 需要一个参数")
		}
		return pluginConfigValue(p, strOf(args[0])), nil
	}))
	_ = pl.Set("configs", rt.jsFn(func(call goja.FunctionCall) (any, error) {
		cfg := GetConfig(p)
		out := make(map[string]any, len(cfg))
		for k, v := range cfg {
			out[k] = v
		}
		return out, nil
	}))
	_ = clv.Set("plugin", pl)

	// clv.log.{info,warn,error}
	logObj := rt.vm.NewObject()
	for _, lv := range []string{"info", "warn", "error"} {
		level := lv
		_ = logObj.Set(level, rt.jsFn(func(call goja.FunctionCall) (any, error) {
			args := argsOf(call)
			parts := make([]string, 0, len(args))
			for _, a := range args {
				parts = append(parts, strOf(a))
			}
			msg := strings.Join(parts, " ")
			util.Log(level, "[插件 %s] %s", p.SlugOf(), msg)
			logPlugin(p.SlugOf(), "log."+level, msg, 0)
			return nil, nil
		}))
	}
	_ = clv.Set("log", logObj)

	// clv.json.{encode,decode}
	jsonObj := rt.vm.NewObject()
	_ = jsonObj.Set("encode", rt.jsFn(func(call goja.FunctionCall) (any, error) {
		args := argsOf(call)
		if len(args) == 0 {
			return "", nil
		}
		return jsonEncode(args[0])
	}))
	_ = jsonObj.Set("decode", rt.jsFn(func(call goja.FunctionCall) (any, error) {
		args := argsOf(call)
		if len(args) == 0 {
			return nil, nil
		}
		return jsonDecode(strOf(args[0]))
	}))
	_ = clv.Set("json", jsonObj)

	return nil
}

// pluginConfigValue 读取插件配置（未设置时回落到清单里的 default）
func pluginConfigValue(p *Plugin, key string) string {
	if key == "" {
		return ""
	}
	if v := models.GetSetting(configKey(p.Name, key)); v != "" {
		return v
	}
	for _, f := range p.Config {
		if f.Key == key {
			return f.Default
		}
	}
	return ""
}

// ---------- 小工具 ----------

// AppVersion 内核版本（config 包不依赖 plugin，直接引用是安全的）
var AppVersion = config.Version

func jsonEncode(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func jsonDecode(s string) (any, error) {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, err
	}
	return v, nil
}
