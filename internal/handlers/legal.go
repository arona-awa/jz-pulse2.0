// 用户协议、隐私政策与使用手册页面。
package handlers

import "net/http"

// TermsPage 用户协议
func TermsPage(w http.ResponseWriter, r *http.Request) {
	Render(w, r, "layout.html", "pg_terms", nil)
}

// PrivacyPage 隐私政策
func PrivacyPage(w http.ResponseWriter, r *http.Request) {
	Render(w, r, "layout.html", "pg_privacy", nil)
}

// ManualPage 使用手册
func ManualPage(w http.ResponseWriter, r *http.Request) {
	Render(w, r, "layout.html", "pg_manual", nil)
}
