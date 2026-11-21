// 应用型插件注册表。
//
// 所有扩展点（路由 / 后台页面与菜单 / Slot / 事件 / 过滤器 / 定时任务）
// 统一登记在此，热路径通过不可变快照读取，避免每次请求加锁。
package plugin

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// RouteReg 应用型插件注册的 HTTP 路由（path 为相对路径）
type RouteReg struct {
	Method string // GET / POST / ...
	Path   string // 相对 "/x/<slug>" 或 "/admin/plugins/<slug>" 的路径
	Fn     string // 脚本函数名
	Auth   string // none | user | admin
	Perm   string // 后台页面所需的内核权限组
	JSON   bool   // JSON 接口（错误以 JSON 返回）
}

// MenuReg 后台菜单项
type MenuReg struct {
	Slug  string // 所属插件 slug（重建快照时填充）
	Label string
	Path  string
	Perm  string
	Order int
}

// PageReg 后台页面
type PageReg struct {
	Path string
	Fn   string
	Perm string
}

// FilterReg 过滤器注册项
type FilterReg struct {
	Fn       string
	Priority int
}

// MiddlewareReg HTTP 中间件注册项（phase: http.before / http.after）
type MiddlewareReg struct {
	Phase string
	Fn    string
}

// JobReg 定时任务
type JobReg struct {
	Name  string
	Every time.Duration
	Fn    string
}

// App 一个已加载的应用型插件实例
type App struct {
	Dir     string // 目录名（生命周期操作的键）
	Plugin  *Plugin
	Worker  *Worker

	Routes      []RouteReg
	Pages       []PageReg
	Menus       []MenuReg
	Slots       map[string][]string
	Events      map[string][]string
	Filters     map[string][]FilterReg
	Jobs        []JobReg
	Middlewares []MiddlewareReg
}

func newApp(dir string, p *Plugin, w *Worker) *App {
	return &App{
		Dir: dir, Plugin: p, Worker: w,
		Slots: map[string][]string{}, Events: map[string][]string{},
		Filters: map[string][]FilterReg{},
	}
}

// ---------- 供热路径读取的不可变快照 ----------

// SlotHook 一个 Slot 处理器
type SlotHook struct {
	App *App
	Fn  string
}

// EventHook 一个事件处理器
type EventHook struct {
	App *App
	Fn  string
}

// FilterHook 一个过滤器
type FilterHook struct {
	App      *App
	Fn       string
	Priority int
}

// MiddlewareHook 一个 HTTP 中间件
type MiddlewareHook struct {
	App *App
	Fn  string
}

// RouteHook 一条路由
type RouteHook struct {
	App   *App
	Route RouteReg
}

type snapshot struct {
	apps        map[string]*App // dir -> app
	bySlug      map[string]*App // slug -> app
	slots       map[string][]SlotHook
	events      map[string][]EventHook
	filters     map[string][]FilterHook
	middlewares map[string][]MiddlewareHook
	routes      []RouteHook
	menus       []MenuReg
	pages       map[string]string // "<slug><path>" -> fn 名
	empty       bool
}

var (
	regMu      sync.Mutex
	regApps    = map[string]*App{} // dir -> app
	regSnap    atomic.Pointer[snapshot]
	regInitOne sync.Once
)

func init() {
	regInitOne.Do(func() { regSnap.Store(&snapshot{empty: true}) })
}

// RegisterApp 登记一个已 setup 完成的应用型插件
func RegisterApp(a *App) {
	regMu.Lock()
	regApps[a.Dir] = a
	regMu.Unlock()
	rebuildSnapshot()
}

// UnregisterApp 移除应用型插件（禁用 / 卸载）
func UnregisterApp(dir string) {
	regMu.Lock()
	delete(regApps, dir)
	regMu.Unlock()
	rebuildSnapshot()
}

// GetApp 按目录名取已加载的应用型插件
func GetApp(dir string) *App {
	regMu.Lock()
	defer regMu.Unlock()
	return regApps[dir]
}

// AppBySlug 按 slug 取已加载的应用型插件
func AppBySlug(slug string) *App {
	s := regSnap.Load()
	if s == nil {
		return nil
	}
	return s.bySlug[slug]
}

// CallFn 调用某个应用型插件的脚本函数（内核与测试使用，参数与返回值可 JSON 化）
func CallFn(slug, fn string, args ...any) (any, error) {
	a := AppBySlug(slug)
	if a == nil || a.Worker == nil {
		return nil, errNoApp
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.Plugin.Timeout())
	defer cancel()
	return a.Worker.Do(ctx, func(rt *jsRuntime) (any, error) {
		rt.cur = nil
		return rt.callFn(fn, args...)
	})
}

// LoadedApps 全部已加载的应用型插件
func LoadedApps() []*App {
	regMu.Lock()
	defer regMu.Unlock()
	out := make([]*App, 0, len(regApps))
	for _, a := range regApps {
		out = append(out, a)
	}
	return out
}

// rebuildSnapshot 重建不可变快照（注册变更时调用，非热路径）
func rebuildSnapshot() {
	regMu.Lock()
	defer regMu.Unlock()
	s := &snapshot{
		apps:        map[string]*App{},
		bySlug:      map[string]*App{},
		slots:       map[string][]SlotHook{},
		events:      map[string][]EventHook{},
		filters:     map[string][]FilterHook{},
		middlewares: map[string][]MiddlewareHook{},
		pages:       map[string]string{},
	}
	for dir, a := range regApps {
		s.apps[dir] = a
		s.bySlug[a.Plugin.SlugOf()] = a
		for slot, fns := range a.Slots {
			for _, fn := range fns {
				s.slots[slot] = append(s.slots[slot], SlotHook{App: a, Fn: fn})
			}
		}
		for ev, fns := range a.Events {
			for _, fn := range fns {
				s.events[ev] = append(s.events[ev], EventHook{App: a, Fn: fn})
			}
		}
		for fname, list := range a.Filters {
			for _, f := range list {
				s.filters[fname] = append(s.filters[fname], FilterHook{App: a, Fn: f.Fn, Priority: f.Priority})
			}
		}
		for _, mw := range a.Middlewares {
			s.middlewares[mw.Phase] = append(s.middlewares[mw.Phase], MiddlewareHook{App: a, Fn: mw.Fn})
		}
		for _, r := range a.Routes {
			s.routes = append(s.routes, RouteHook{App: a, Route: r})
		}
		for _, m := range a.Menus {
			m.Slug = a.Plugin.SlugOf()
			s.menus = append(s.menus, m)
		}
		for _, p := range a.Pages {
			s.pages[a.Plugin.SlugOf()+p.Path] = p.Fn
		}
	}
	// 过滤器按优先级升序（小的先执行）
	for k := range s.filters {
		list := s.filters[k]
		sort.SliceStable(list, func(i, j int) bool { return list[i].Priority < list[j].Priority })
		s.filters[k] = list
	}
	sort.SliceStable(s.menus, func(i, j int) bool { return s.menus[i].Order < s.menus[j].Order })
	s.empty = len(s.apps) == 0
	regSnap.Store(s)
}

func currentSnapshot() *snapshot {
	s := regSnap.Load()
	if s == nil {
		return &snapshot{empty: true}
	}
	return s
}

// HasApps 是否有任何应用型插件在运行（热路径零开销判断）
func HasApps() bool { return !currentSnapshot().empty }

// ---------- 注册表查询（内核侧使用） ----------

// SlotHooks 取某 Slot 的全部处理器
func SlotHooks(name string) []SlotHook { return currentSnapshot().slots[name] }

// EventHooks 取某事件的全部处理器
func EventHooks(event string) []EventHook { return currentSnapshot().events[event] }

// FilterHooks 取某过滤器的全部处理器（已按优先级排序）
func FilterHooks(name string) []FilterHook { return currentSnapshot().filters[name] }

// AdminMenus 全部插件后台菜单
func AdminMenus() []MenuReg { return currentSnapshot().menus }

// MiddlewareHooks 取某阶段（http.before / http.after）的中间件
func MiddlewareHooks(phase string) []MiddlewareHook { return currentSnapshot().middlewares[phase] }

// FindRoute 匹配插件路由：返回 App、路由定义与剩余路径
func FindRoute(prefix string, method, path string) (*App, *RouteReg, bool) {
	s := currentSnapshot()
	for i := range s.routes {
		r := &s.routes[i]
		if r.App.Plugin.SlugOf() != prefix {
			continue
		}
		if !methodEqual(r.Route.Method, method) {
			continue
		}
		if pathEqual(r.Route.Path, path) {
			return r.App, &r.Route, true
		}
	}
	return nil, nil, false
}

// AdminPageFn 查后台页面处理器
func AdminPageFn(slug, path string) (fn string, ok bool) {
	fn, ok = currentSnapshot().pages[slug+path]
	return
}

func methodEqual(reg, got string) bool {
	if reg == "" || reg == "*" {
		return true
	}
	return equalFoldASCII(reg, got)
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'a' <= ca && ca <= 'z' {
			ca -= 32
		}
		if 'a' <= cb && cb <= 'z' {
			cb -= 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// pathEqual 路径匹配，支持 "/*" 后缀通配
func pathEqual(reg, got string) bool {
	if reg == got {
		return true
	}
	if len(reg) > 2 && reg[len(reg)-2:] == "/*" {
		return len(got) >= len(reg)-2 && got[:len(reg)-2] == reg[:len(reg)-2]
	}
	return false
}
