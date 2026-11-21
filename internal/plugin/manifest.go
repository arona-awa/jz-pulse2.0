// 插件形态与清单解析。
//
// 两种形态（由 plugin.json 的 kind 字段区分）：
//   - 声明式 declarative（默认，无 kind 字段）：能力来自 hooks 声明，内核解释执行
//   - 应用型 app（kind=app）：能力来自 main.js 中的 clv.* 运行时注册
//
// 设计原则：清单决定「能碰什么」（权限必须先于脚本加载确定），
// 脚本决定「要做什么」（注册逻辑本身是代码，写成声明会退化成白名单枚举）。
package plugin

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 插件形态
const (
	KindDeclarative = "declarative"
	KindApp         = "app"
)

// RuntimeSpec 应用型插件的运行时声明
type RuntimeSpec struct {
	Engine    string `json:"engine"`     // 当前仅支持 "js"
	Entry     string `json:"entry"`      // 入口脚本（相对插件目录）
	TimeoutMS int    `json:"timeout_ms"` // 单次调用超时（毫秒），默认 1500，硬上限 5000
}

const (
	defaultTimeoutMS = 1500
	// maxTimeoutMS 单次调用超时硬上限。
	// 放宽到 30s 是为了支持插件做耗时操作（如调用外部 AI 接口），
	// 默认值仍是 1500ms，只有显式声明才会放宽。
	maxTimeoutMS   = 30000
	setupTimeoutMS = 3000 // setup() 允许略长
	setupMaxMS     = 5000 // setup() 的上限：初始化不应耗时过长
)

// KindOf 返回插件形态（缺省为声明式，保证旧插件行为不变）
func (p *Plugin) KindOf() string {
	if p != nil && strings.EqualFold(strings.TrimSpace(p.Kind), KindApp) {
		return KindApp
	}
	return KindDeclarative
}

// IsApp 是否为应用型插件
func (p *Plugin) IsApp() bool { return p.KindOf() == KindApp }

// SlugOf 返回 URL / 表名安全标识：显式声明优先，否则由 name 归一化，
// 再退化为 plugin-<sha1 前 8 位>，保证任何插件名（含中文）都有可用标识。
func (p *Plugin) SlugOf() string {
	if p == nil {
		return ""
	}
	if s := NormalizeSlug(p.Slug); s != "" {
		return s
	}
	if s := NormalizeSlug(p.Name); s != "" {
		return s
	}
	sum := sha1.Sum([]byte(p.Name))
	return "plugin-" + hex.EncodeToString(sum[:4])
}

// NormalizeSlug 归一化为仅含 [a-z0-9_-] 的标识（连续非法字符折叠为一个 '-'）
func NormalizeSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_':
			b.WriteRune(r)
			dash = false
		case r == '-':
			if !dash {
				b.WriteByte('-')
				dash = true
			}
		default: // 中文、空格等一律折叠为 '-'
			if !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// Timeout 单次脚本调用超时
func (p *Plugin) Timeout() time.Duration {
	ms := defaultTimeoutMS
	if p != nil && p.Runtime != nil && p.Runtime.TimeoutMS > 0 {
		ms = p.Runtime.TimeoutMS
	}
	if ms <= 0 || ms > maxTimeoutMS {
		ms = defaultTimeoutMS
	}
	return time.Duration(ms) * time.Millisecond
}

// HasPermission 插件是否声明了指定权限（应用型的能力白名单）
func (p *Plugin) HasPermission(name string) bool {
	if p == nil {
		return false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, v := range p.Permissions {
		if strings.EqualFold(strings.TrimSpace(v), name) {
			return true
		}
	}
	return false
}

// TablePrefix 插件数据表前缀：pl_<slug>_（slug 中的 '-' 转为 '_'，避免反引号）
func (p *Plugin) TablePrefix() string {
	return "pl_" + strings.ReplaceAll(p.SlugOf(), "-", "_") + "_"
}

// TableName 由逻辑名得到实际表名
func (p *Plugin) TableName(logical string) string {
	logical = NormalizeSlug(logical)
	if logical == "" {
		return ""
	}
	return p.TablePrefix() + strings.ReplaceAll(logical, "-", "_")
}

// unmarshalPlugin 解析插件清单（要求 UTF-8 无 BOM，带 BOM 会给出明确提示）
func unmarshalPlugin(data []byte, p *Plugin) error {
	if len(data) > 1<<20 {
		return errors.New("plugin.json 过大（超过 1MB）")
	}
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return errors.New("plugin.json 含有 UTF-8 BOM，请另存为「UTF-8 无 BOM」")
	}
	if err := json.Unmarshal(data, p); err != nil {
		return fmt.Errorf("plugin.json 解析失败: %w", err)
	}
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("plugin.json 缺少 name 字段")
	}
	return nil
}

// entryPath 校验并返回入口脚本的安全相对路径
func (p *Plugin) entryPath() string {
	if p == nil || p.Runtime == nil {
		return ""
	}
	name := strings.TrimSpace(p.Runtime.Entry)
	if name == "" {
		return ""
	}
	name = strings.ReplaceAll(name, "\\", "/")
	// 只允许插件目录下的直接路径，拒绝穿越与绝对路径
	if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
		return ""
	}
	return name
}
