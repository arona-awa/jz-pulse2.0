// Package plugin 插件系统，支持两种形态（由 plugin.json 的 kind 字段区分）：
//
//   - 声明式（默认，无 kind）：能力来自 hooks 声明，由内核解释执行
//     html   : 向页面钩子点注入 HTML（header_html / footer_html / admin_plugins_top）
//     filter : 正则改写帖子/评论内容（content_filter）
//     http   : 事件 Webhook（post_created / comment_created / report_created）
//     guard  : 发帖守卫（compose_guard）
//     badge  : 帖子标识（post_badge）
//   - 应用型（kind=app）：能力来自 main.js 中的 clv.* 运行时注册
//     （路由 / 数据表 / 后台菜单与页面 / Slot / 事件 / 过滤器 / 中间件 / 定时任务）
//
// 插件为目录 data/plugins/<name>/plugin.json（支持 zip 上传安装）。
// 开发文档：docs/PLUGIN.md（总览）、docs/PLUGIN-DECLARATIVE.md、docs/PLUGIN-APP.md。
package plugin

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"clearlove/internal/config"
	"clearlove/internal/models"
	"clearlove/internal/util"
)

// ConfigField 插件配置项声明。后台「插件管理」会按此自动渲染设置表单，
// 配置值保存在 settings 表中（键名前缀 plugin.<插件名>.），可在钩子中通过
// {config:字段名} 占位符引用。
type ConfigField struct {
	Key     string   `json:"key"`     // 字段名（供 {config:key} 引用）
	Label   string   `json:"label"`   // 显示名称
	Type    string   `json:"type"`    // text | textarea | switch | select | number
	Default string   `json:"default"` // 默认值
	Help    string   `json:"help"`    // 说明文字
	Options []string `json:"options"` // select 型的候选项
}

// HookDef 单个钩子定义
type HookDef struct {
	Hook    string `json:"hook"`    // 钩子名
	Type    string `json:"type"`    // html | filter | http | guard | badge
	HTML    string `json:"html"`    // html 型：注入片段
	Match   string `json:"match"`   // filter 型：正则
	Replace string `json:"replace"` // filter 型：替换文本
	URL     string `json:"url"`     // http 型：Webhook 地址

	// guard / badge 型专用
	Nicknames string `json:"nicknames"` // 触发昵称，逗号分隔，支持 {config:key}
	Require   string `json:"require"`   // admin = 必须验证后台账号密码
	Label     string `json:"label"`     // 命中后写入帖子的标识文字
	Message   string `json:"message"`   // 校验失败时的提示语
}

// Plugin 插件清单。支持两种形态：
//   - 声明式（默认，无 kind 字段）：能力来自 hooks 声明，内核解释执行
//   - 应用型（kind=app）：能力来自 main.js 中的 clv.* 运行时注册
type Plugin struct {
	Kind        string        `json:"kind"`        // "" / declarative = 声明式；app = 应用型
	Name        string        `json:"name"`        // 插件名（同时作为安装目录名）
	Slug        string        `json:"slug"`        // URL / 表名安全标识，缺省由 Name 归一化
	Version     string        `json:"version"`     // 版本号，展示用
	Author      string        `json:"author"`      // 作者
	Description string        `json:"description"` // 简介
	Requires    string        `json:"requires"`    // 建议的最低表白墙版本（仅提示）
	Runtime     *RuntimeSpec  `json:"runtime"`     // 应用型必填：脚本入口与超时
	Permissions []string      `json:"permissions"` // 应用型能力白名单（必须在清单中声明）
	Config      []ConfigField `json:"config"`      // 配置项声明（两种形态都支持）
	Hooks       []HookDef     `json:"hooks"`       // 声明式钩子（应用型可不写）
}

// Meta 插件目录信息（管理页展示）
type Meta struct {
	Plugin
	Enabled bool
	Warn    string // 兼容性提示：使用了当前版本不支持的钩子 / 版本要求不满足
	App     bool   // 是否应用型（kind=app）
	Running bool   // 应用型插件运行时是否已加载
	Slug    string // 应用型插件的 URL/表名标识
}

// supportedTypes 当前内核支持的钩子类型
var supportedTypes = map[string]bool{
	"html": true, "filter": true, "http": true, "guard": true, "badge": true,
}

// checkCompat 返回插件的兼容性提示（空串表示没有问题）
func (p *Plugin) checkCompat() string {
	var bad []string
	seen := map[string]bool{}
	for _, h := range p.Hooks {
		if h.Type == "" || supportedTypes[h.Type] || seen[h.Type] {
			continue
		}
		seen[h.Type] = true
		bad = append(bad, h.Type)
	}
	if len(bad) > 0 {
		return "该插件使用了当前版本不支持的钩子类型：" + strings.Join(bad, "、") + "，请升级表白墙后再启用"
	}
	if p.Requires != "" && versionLess(config.Version, p.Requires) {
		return "该插件要求表白墙版本 ≥ " + p.Requires + "，当前版本为 " + config.Version
	}
	return ""
}

// versionLess 按 x.y.z 逐段比较版本号（缺失段按 0 处理）
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, y := 0, 0
		if i < len(pa) {
			x, _ = strconv.Atoi(strings.TrimSpace(pa[i]))
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(strings.TrimSpace(pb[i]))
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// dir 插件目录（跟随数据目录配置，支持 CLEARLOVE_DATA_DIR 自定义）
func dir() string { return filepath.Join(config.Cfg.DataDir, "plugins") }

// ---------- 清单缓存 ----------
//
// 热路径（每次页面渲染、每次发帖）都会读取插件清单，
// 这里加 5 秒短缓存；安装/启用/禁用/卸载等管理操作会立即失效缓存，
// 因此管理端行为与"每次读盘"没有可感知差异。

var loadCache struct {
	sync.Mutex
	exp  time.Time
	data map[string]*Plugin
}

// cachedPlugins 热路径使用的插件清单（带短 TTL）
func cachedPlugins() map[string]*Plugin {
	loadCache.Lock()
	defer loadCache.Unlock()
	if loadCache.data != nil && time.Now().Before(loadCache.exp) {
		return loadCache.data
	}
	loadCache.data = Load()
	loadCache.exp = time.Now().Add(5 * time.Second)
	return loadCache.data
}

// InvalidatePluginCache 管理操作后主动失效缓存（安装/启用/禁用/卸载/配置变更）
func InvalidatePluginCache() {
	loadCache.Lock()
	loadCache.exp = time.Time{}
	loadCache.Unlock()
	filterCache.Lock()
	filterCache.exp = time.Time{}
	filterCache.Unlock()
}

// Load 读取磁盘上的全部插件清单
func Load() map[string]*Plugin {
	out := map[string]*Plugin{}
	entries, err := os.ReadDir(dir())
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir(), e.Name(), "plugin.json"))
		if err != nil {
			continue
		}
		var p Plugin
		if json.Unmarshal(b, &p) == nil && p.Name != "" {
			loadHookFiles(&p, filepath.Join(dir(), e.Name()))
			out[p.Name] = &p
		}
	}
	return out
}

// loadHookFiles 支持把 html 片段放在插件目录的文件中：
//
//	{"hook":"footer_html","type":"html","html":"@player.html"}
//
// 表示读取同目录下的 player.html。带脚本的插件用它可以避免在 JSON 里
// 转义大段 HTML，插件安装（zip）时会连同该文件一起落盘。
func loadHookFiles(p *Plugin, base string) {
	for i := range p.Hooks {
		h := &p.Hooks[i]
		if h.Type != "html" || !strings.HasPrefix(h.HTML, "@") {
			continue
		}
		// 只允许插件目录下的直接文件名，避免路径穿越
		name := filepath.Base(strings.TrimSpace(h.HTML[1:]))
		if name == "" || name == "." || name == string(filepath.Separator) {
			h.HTML = ""
			continue
		}
		b, err := os.ReadFile(filepath.Join(base, name))
		if err != nil {
			// 文件缺失时清空，避免把 "@xxx" 原样注入页面
			h.HTML = ""
			continue
		}
		h.HTML = string(b)
	}
}

// enabledSet 已启用插件名集合
func enabledSet() map[string]bool {
	set := map[string]bool{}
	var list []string
	if s := models.GetSetting("enabled_plugins"); s != "" {
		_ = json.Unmarshal([]byte(s), &list)
	}
	for _, v := range list {
		set[v] = true
	}
	return set
}

func saveEnabled(set map[string]bool) {
	var list []string
	for k := range set {
		list = append(list, k)
	}
	b, _ := json.Marshal(list)
	_ = models.SetSetting("enabled_plugins", string(b))
}

// List 管理页用：全部插件、启用状态、形态与运行状态
func List() []Meta {
	enabled := enabledSet()
	var out []Meta
	for name, p := range Load() {
		m := Meta{Plugin: *p, Enabled: enabled[p.Name], Warn: p.checkCompat()}
		if p.IsApp() {
			m.App = true
			m.Slug = p.SlugOf()
			m.Running = GetApp(filepath.Base(name)) != nil
		}
		out = append(out, m)
	}
	return out
}

// ---------- 插件配置 ----------

// configKey 插件配置在 settings 表中的键名
func configKey(pluginName, key string) string {
	return "plugin." + pluginName + "." + key
}

// GetConfig 读取插件配置（未设置的字段回落到声明的默认值）
func GetConfig(p *Plugin) map[string]string {
	out := make(map[string]string, len(p.Config))
	for _, f := range p.Config {
		if f.Key == "" {
			continue
		}
		v := models.GetSetting(configKey(p.Name, f.Key))
		if v == "" {
			v = f.Default
		}
		out[f.Key] = v
	}
	return out
}

// ConfigOf 按插件名读取配置（插件不存在时返回空表）
func ConfigOf(pluginName string) map[string]string {
	if p, ok := Load()[pluginName]; ok {
		return GetConfig(p)
	}
	return map[string]string{}
}

// SaveConfig 保存插件配置（只接受该插件声明过的字段）
func SaveConfig(p *Plugin, values map[string]string) error {
	for _, f := range p.Config {
		if f.Key == "" {
			continue
		}
		if v, ok := values[f.Key]; ok {
			if err := models.SetSetting(configKey(p.Name, f.Key), util.StripHTML(v)); err != nil {
				return err
			}
		}
	}
	return nil
}

// reConfigVar 配置占位符 {config:key}
var reConfigVar = regexp.MustCompile(`\{config:[A-Za-z0-9_\-]+\}`)

// expand 展开 {config:key} 占位符（用于昵称名单、标签、提示语等非 HTML 场景，原样替换）
func expand(s string, cfg map[string]string) string {
	return expandIn(s, cfg, false)
}

// expandHTML 展开 {config:key} 占位符，配置值做 HTML 转义，
// 防止管理员配置值（如 {config:url}）携带引号/尖括号闭合标签属性造成存储型 XSS
func expandHTML(s string, cfg map[string]string) string {
	return expandIn(s, cfg, true)
}

func expandIn(s string, cfg map[string]string, esc bool) string {
	if s == "" || !strings.Contains(s, "{config:") {
		return s
	}
	return reConfigVar.ReplaceAllStringFunc(s, func(m string) string {
		v := cfg[m[len("{config:"):len(m)-1]]
		if esc {
			return html.EscapeString(v)
		}
		return v
	})
}

// splitNames 拆分逗号/分号/换行分隔的名单
func splitNames(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if v := strings.TrimSpace(f); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// ---------- 发帖守卫（guard）与帖子标识（badge） ----------

// GuardResult 发帖守卫判定结果
type GuardResult struct {
	Require bool     // true = 必须验证后台管理员账号密码
	Labels  []string // 通过后写入帖子的标识文字
	Message string   // 未通过时的提示语
}

// CheckComposeGuard 检查昵称是否命中任一启用插件的发帖守卫（compose_guard）。
// 未命中返回 nil。多个插件同时命中时会合并标识与要求。
func CheckComposeGuard(nickname string) *GuardResult {
	name := strings.TrimSpace(nickname)
	if name == "" {
		return nil
	}
	enabled := enabledSet()
	res := &GuardResult{}
	hit := false
	for pname, p := range cachedPlugins() {
		if !enabled[pname] {
			continue
		}
		cfg := GetConfig(p)
		for _, h := range p.Hooks {
			if h.Type != "guard" || h.Hook != "compose_guard" {
				continue
			}
			matched := false
			for _, n := range splitNames(expand(h.Nicknames, cfg)) {
				if strings.EqualFold(n, name) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			hit = true
			if h.Require == "admin" {
				res.Require = true
			}
			if h.Label != "" {
				res.Labels = append(res.Labels, expand(h.Label, cfg))
			}
			if res.Message == "" && h.Message != "" {
				res.Message = expand(h.Message, cfg)
			}
		}
	}
	if !hit {
		return nil
	}
	return res
}

// GuardNicknames 返回所有「需要验证后台账号」的守卫昵称。
// 发帖页据此在提交前弹出账号验证弹窗，避免用户提交后才看到报错。
func GuardNicknames() []string {
	enabled := enabledSet()
	var out []string
	for pname, p := range cachedPlugins() {
		if !enabled[pname] {
			continue
		}
		cfg := GetConfig(p)
		for _, h := range p.Hooks {
			if h.Type != "guard" || h.Hook != "compose_guard" || h.Require != "admin" {
				continue
			}
			out = append(out, splitNames(expand(h.Nicknames, cfg))...)
		}
	}
	return out
}

// NicknameBadges 返回该昵称在发帖时应携带的标识（post_badge 钩子）
func NicknameBadges(nickname string) []string {
	name := strings.TrimSpace(nickname)
	if name == "" {
		return nil
	}
	enabled := enabledSet()
	var out []string
	for pname, p := range cachedPlugins() {
		if !enabled[pname] {
			continue
		}
		cfg := GetConfig(p)
		for _, h := range p.Hooks {
			if h.Type != "badge" || h.Hook != "post_badge" || h.Label == "" {
				continue
			}
			for _, n := range splitNames(expand(h.Nicknames, cfg)) {
				if strings.EqualFold(n, name) {
					out = append(out, expand(h.Label, cfg))
					break
				}
			}
		}
	}
	return out
}

// Install 从上传的 zip 安装插件（zip 内应包含 plugin.json）
func Install(filename string, data []byte) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("无效的 zip 包: %w", err)
	}
	// 先定位 plugin.json 确定插件名
	var manifest []byte
	for _, f := range zr.File {
		base := filepath.Base(f.Name)
		if base == "plugin.json" {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			manifest, _ = io.ReadAll(io.LimitReader(rc, 1<<20))
			rc.Close()
			break
		}
	}
	if manifest == nil {
		return fmt.Errorf("压缩包中未找到 plugin.json")
	}
	var p Plugin
	if err := json.Unmarshal(manifest, &p); err != nil || p.Name == "" {
		return fmt.Errorf("plugin.json 解析失败（需包含 name 字段）")
	}
	// 防目录穿越：仅接受安全的相对路径
	target := filepath.Join(dir(), filepath.Base(p.Name))
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	for _, f := range zr.File {
		name := filepath.Clean(f.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue
		}
		dst := filepath.Join(target, name)
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(dst, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(dst)
		if err != nil {
			rc.Close()
			return err
		}
		_, _ = io.Copy(out, io.LimitReader(rc, 50<<20))
		out.Close()
		rc.Close()
	}
	InvalidatePluginCache()
	return nil
}

// Remove 卸载插件（目录删除并从启用列表移除）
func Remove(name string) error {
	_ = os.RemoveAll(filepath.Join(dir(), filepath.Base(name)))
	set := enabledSet()
	delete(set, name)
	saveEnabled(set)
	InvalidatePluginCache()
	return nil
}

// SetEnabled 启用/禁用插件
func SetEnabled(name string, on bool) {
	set := enabledSet()
	if on {
		set[name] = true
	} else {
		delete(set, name)
	}
	saveEnabled(set)
	InvalidatePluginCache()
}

var filterCache struct {
	sync.Mutex
	rules []compiledFilter
	exp   time.Time
}

type compiledFilter struct {
	re  string // 原始正则（编译失败则跳过）
	rep string
}

// CallFilter 应用所有启用插件的 content_filter 钩子改写内容
func CallFilter(content string) string {
	if content == "" {
		return content
	}
	filterCache.Lock()
	defer filterCache.Unlock()
	if time.Now().After(filterCache.exp) {
		filterCache.rules = nil
		enabled := enabledSet()
		for name, p := range cachedPlugins() {
			if !enabled[name] {
				continue
			}
			for _, h := range p.Hooks {
				if h.Hook == "content_filter" && h.Type == "filter" && h.Match != "" {
					filterCache.rules = append(filterCache.rules, compiledFilter{re: h.Match, rep: h.Replace})
				}
			}
		}
		filterCache.exp = time.Now().Add(30 * time.Second)
	}
	for _, r := range filterCache.rules {
		if re, err := regexp.Compile(r.re); err == nil {
			content = re.ReplaceAllString(content, r.rep)
		}
	}
	return content
}

// CallHTML 收集指定页面钩子点的全部启用插件 HTML 片段，
// 并展开片段中的 {config:key} 占位符（与 guard / badge 钩子保持一致），
// 插件据此把后台配置好的值（如上传的音乐地址）注入页面。
func CallHTML(hook string) string {
	var b strings.Builder
	enabled := enabledSet()
	for name, p := range cachedPlugins() {
		if !enabled[name] {
			continue
		}
		cfg := GetConfig(p)
		for _, h := range p.Hooks {
			if h.Hook == hook && h.Type == "html" {
				b.WriteString(expandHTML(h.HTML, cfg))
			}
		}
	}
	return b.String()
}

// Notify 异步触发事件 Webhook（POST JSON），失败仅记录日志
func Notify(event string, payload map[string]any) {
	enabled := enabledSet()
	for name, p := range cachedPlugins() {
		if !enabled[name] {
			continue
		}
		for _, h := range p.Hooks {
			if h.Hook == event && h.Type == "http" && h.URL != "" {
				go postWebhook(h.URL, event, payload)
			}
		}
	}
}

func postWebhook(url, event string, payload map[string]any) {
	// 拷贝后再补充元数据字段：payload 会被多个 webhook goroutine 与内核共享，
	// 直接写入原 map 会触发并发写 panic
	body, _ := json.Marshal(withMeta(event, payload))
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ClearLove-Event", event)
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	} else {
		util.Log("warn", "插件 Webhook %s 失败: %v", event, err)
	}
}
