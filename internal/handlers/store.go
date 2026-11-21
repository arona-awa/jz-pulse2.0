// 插件商店：对接 ClearLove 云端插件社区（默认 https://clearlove.kazx.top）。
// 管理员可在后台浏览云端插件市场，一键下载安装（SHA256 校验）。
package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"clearlove/internal/models"
	"clearlove/internal/plugin"
)

// defaultCloudAPI 云端默认地址（可在后台「网站设置」中修改 cloud_api）
const defaultCloudAPI = "https://clearlove.kazx.top"

// cloudBase 返回云端服务地址（去掉末尾斜杠）
func cloudBase() string {
	v := strings.TrimSpace(models.GetSetting("cloud_api"))
	if v == "" {
		return defaultCloudAPI
	}
	return strings.TrimSuffix(v, "/")
}

// StorePlugin 云端插件条目（与云端 /api/v1/store/plugins 返回结构一致）
type StorePlugin struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Author      string `json:"author"`
	Category    string `json:"category"`
	CategoryTxt string `json:"category_label"`
	Tags        string `json:"tags"`
	Downloads   int64  `json:"downloads"`
	Installs    int64  `json:"installs"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	UpdatedAt   string `json:"updated_at"`
	PageURL     string `json:"page_url"`
	DownloadURL string `json:"download_url"`
	// 应用型插件（kind=app）的形态与权限声明，供安装前审阅
	Kind        string   `json:"kind"`
	Permissions []string `json:"permissions"`
}

// fetchStoreList 拉取云端插件列表，返回列表与错误提示。
// kind 为空表示全部形态，可传 app（应用型）/ declarative（声明式）。
func fetchStoreList(q, kind string) ([]StorePlugin, string) {
	u := cloudBase() + "/api/v1/store/plugins?limit=50"
	if q != "" {
		u += "&q=" + url.QueryEscape(q)
	}
	if kind != "" {
		u += "&kind=" + url.QueryEscape(kind)
	}
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return nil, "无法连接云端插件社区（" + cloudBase() + "），请检查网络或云端地址配置"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "云端返回状态码 " + resp.Status
	}
	var out struct {
		OK      bool          `json:"ok"`
		Plugins []StorePlugin `json:"plugins"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, "云端返回数据解析失败"
	}
	return out.Plugins, ""
}

// AdminStore 插件商店页面
func AdminStore(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermPlugin); a == nil {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind != "app" && kind != "declarative" {
		kind = ""
	}
	list, errMsg := fetchStoreList(q, kind)
	// 本地已安装插件（按 plugin.json 的 name 匹配）
	installed := map[string]bool{}
	for _, p := range plugin.List() {
		installed[p.Name] = true
	}
	appNum := 0
	for _, sp := range list {
		if sp.Kind == "app" {
			appNum++
		}
	}
	Render(w, r, "admin_layout.html", "pg_admin_store", map[string]any{
		"plugins":   list,
		"installed": installed,
		"q":         q,
		"kind":      kind,
		"appNum":    appNum,
		"cloud":     cloudBase(),
		"errMsg":    errMsg,
		"notice":    r.URL.Query().Get("msg"),
	})
}

// AdminStoreInstall 从云端下载并安装插件（校验 SHA256 后落盘）
func AdminStoreInstall(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermPlugin); a == nil {
		return
	}
	slug := strings.TrimSpace(r.FormValue("slug"))
	version := strings.TrimSpace(r.FormValue("version"))
	wantSHA := strings.TrimSpace(r.FormValue("sha256"))
	back := func(msg string) {
		http.Redirect(w, r, "/admin/store?err="+url.QueryEscape(msg), http.StatusSeeOther)
	}
	if slug == "" || version == "" {
		back("缺少插件信息")
		return
	}
	dl := cloudBase() + "/api/v1/store/download/" + url.PathEscape(slug) + "/" + url.PathEscape(version)
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(dl)
	if err != nil {
		back("下载失败：无法连接云端")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		back("下载失败：云端返回 " + resp.Status)
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if err != nil || len(data) == 0 {
		back("下载失败：文件为空或读取异常")
		return
	}
	// 完整性校验
	sum := sha256.Sum256(data)
	if wantSHA != "" && hex.EncodeToString(sum[:]) != wantSHA {
		back("安装包校验失败（SHA256 不匹配），已阻止安装")
		return
	}
	if err := plugin.Install(slug+"-"+version+".zip", data); err != nil {
		back("安装失败：" + err.Error())
		return
	}
	go reportInstall(slug, version)
	http.Redirect(w, r, "/admin/store?msg="+url.QueryEscape("插件「"+slug+"」安装成功，请在插件管理中启用"), http.StatusSeeOther)
}

// reportInstall 异步上报安装量（失败不影响本地安装）
func reportInstall(slug, version string) {
	body, _ := json.Marshal(map[string]string{
		"slug":    slug,
		"version": version,
		"site":    models.GetSetting("site_name"),
	})
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest(http.MethodPost, cloudBase()+"/api/v1/store/report", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}
