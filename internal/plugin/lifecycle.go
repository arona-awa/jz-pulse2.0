// 应用型插件生命周期：加载（setup 注册）、卸载、启动时批量引导。
package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clearlove/internal/util"
)

// Boot 启动时加载全部已启用的应用型插件（A 型插件无需加载，按请求即时解析）
func Boot() {
	for name, p := range Load() {
		if !p.IsApp() {
			continue
		}
		if !enabledSet()[p.Name] {
			continue
		}
		if err := LoadApp(name); err != nil {
			util.Log("error", "应用型插件 %s 加载失败: %v", name, err)
			logPlugin(p.SlugOf(), "load_error", err.Error(), 0)
		}
	}
}

// LoadApp 加载并启动一个应用型插件（幂等：重复调用会先卸载）
func LoadApp(dir string) error {
	dir = filepath.Base(strings.TrimSpace(dir))
	if dir == "" || dir == "." {
		return errors.New("无效的插件目录")
	}
	full := filepath.Join(dir_pluginsDir(), dir)
	data, err := os.ReadFile(filepath.Join(full, "plugin.json"))
	if err != nil {
		return fmt.Errorf("读取 plugin.json 失败: %w", err)
	}
	var p Plugin
	if err := unmarshalPlugin(data, &p); err != nil {
		return err
	}
	if !p.IsApp() {
		return errors.New("该插件不是应用型（kind != app），无需加载运行时")
	}
	if p.Runtime == nil || strings.TrimSpace(p.Runtime.Engine) == "" {
		return errors.New("缺少 runtime.engine（当前支持 js）")
	}
	if !strings.EqualFold(strings.TrimSpace(p.Runtime.Engine), "js") {
		return fmt.Errorf("不支持的运行时引擎 %q（当前仅支持 js）", p.Runtime.Engine)
	}

	// 幂等：先卸载旧实例
	UnloadApp(dir)

	rt, err := newJSRuntime(full, &p)
	if err != nil {
		return err
	}
	worker := NewWorker(rt)
	app := newApp(dir, &p, worker)
	rt.app = app // Host API 用它做权限校验与注册

	// 1) 执行入口脚本（顶层只做定义）
	ctx, cancel := context.WithTimeout(context.Background(), p.Timeout())
	err = loadEntryWith(worker, ctx, rt)
	cancel()
	if err != nil {
		worker.Close()
		return err
	}

	// 2) 生命周期：安装回调（仅当标记为首次）
	ctx2, cancel2 := context.WithTimeout(context.Background(), p.Timeout())
	callOptional(worker, ctx2, rt, "onInstall")
	cancel2()

	// 3) setup：注册全部扩展点
	setupCtx, cancelSetup := context.WithTimeout(context.Background(), setupTimeoutBudget(&p))
	err = runSetup(worker, setupCtx, rt)
	cancelSetup()
	if err != nil {
		worker.Close()
		return fmt.Errorf("setup() 执行失败: %w", err)
	}

	// 4) 生成注册表快照并启动任务
	RegisterApp(app)
	startJobs(app)
	noteSuccess(app)
	logPlugin(p.SlugOf(), "enable", "插件已加载", 0)
	util.Log("info", "应用型插件 %s(%s) 已加载：路由 %d、后台页 %d、菜单 %d、slot %d、事件 %d、过滤器 %d、任务 %d",
		p.Name, p.SlugOf(), len(app.Routes), len(app.Pages), len(app.Menus),
		len(app.Slots), len(app.Events), len(app.Filters), len(app.Jobs))
	return nil
}

// UnloadApp 停止并移除一个应用型插件
func UnloadApp(dir string) {
	dir = filepath.Base(strings.TrimSpace(dir))
	if dir == "" || dir == "." {
		return
	}
	if a := GetApp(dir); a != nil {
		ctx, cancel := context.WithTimeout(context.Background(), a.Plugin.Timeout())
		callOptional(a.Worker, ctx, a.Worker.rt, "onDisable")
		cancel()
		stopJobs(dir)
		a.Worker.Close()
		logPlugin(a.Plugin.SlugOf(), "disable", "插件已卸载", 0)
	}
	UnregisterApp(dir)
}

// ReloadApp 重载（用于开发调试：修改脚本后重新 setup）
func ReloadApp(dir string) error {
	UnloadApp(dir)
	return LoadApp(dir)
}

// loadEntryWith 在 worker 中执行入口脚本
func loadEntryWith(w *Worker, ctx context.Context, rt *jsRuntime) error {
	_, err := w.Do(ctx, func(r *jsRuntime) (any, error) {
		return nil, r.loadEntry()
	})
	return err
}

// runSetup 在 worker 中执行 setup()
func runSetup(w *Worker, ctx context.Context, rt *jsRuntime) error {
	_, err := w.Do(ctx, func(r *jsRuntime) (any, error) {
		if !r.hasFn("setup") {
			return nil, errors.New("应用型插件必须定义 setup() 函数")
		}
		_, err := r.callFn("setup")
		return nil, err
	})
	return err
}

// callOptional 调用可选的生命周期回调（未定义或出错都不影响主流程）
func callOptional(w *Worker, ctx context.Context, rt *jsRuntime, fn string) {
	if w == nil || rt == nil {
		return
	}
	_, err := w.Do(ctx, func(r *jsRuntime) (any, error) {
		if !r.hasFn(fn) {
			return nil, nil
		}
		_, err := r.callFn(fn)
		return nil, err
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		util.Log("warn", "插件回调 %s 执行失败: %v", fn, err)
	}
}

// setupTimeoutBudget setup 允许稍长一点的时间，但不应像运行时那样放长
// （初始化只做注册，耗时的网络调用应放在路由/任务里）
func setupTimeoutBudget(p *Plugin) time.Duration {
	d := p.Timeout()
	if d > time.Duration(setupMaxMS)*time.Millisecond {
		return time.Duration(setupMaxMS) * time.Millisecond
	}
	if d < time.Duration(setupTimeoutMS)*time.Millisecond {
		return time.Duration(setupTimeoutMS) * time.Millisecond
	}
	return d
}

// dir_pluginsDir 插件根目录（跟随数据目录配置）
func dir_pluginsDir() string { return dir() }
