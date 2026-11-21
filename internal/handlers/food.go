// 必吃榜：店铺发布、评分与评价。
package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"clearlove/internal/database"
	"clearlove/internal/models"
	"clearlove/internal/util"
)

var foodTags = []string{"正餐", "下午茶", "甜点", "奶茶", "小吃", "夜宵"}

// FoodPlace 必吃榜店铺
type FoodPlace struct {
	ID          int64
	UserID      int64
	ShopName    string
	Location    string
	Description string
	Tag         string
	Image       string
	CreatedAt   string
	Avg         float64
	RatingCount int
	MyScore     int
}

// AvgPct 评分星条宽度百分比（0-100）
func (p FoodPlace) AvgPct() int {
	v := int(math.Round(p.Avg / 5 * 100))
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return v
}

// FoodReview 店铺评价
type FoodReview struct {
	ID        int64
	UserID    int64
	Nickname  string
	Avatar    string
	Content   string
	Level     int
	CreatedAt string
}

func validFoodTag(tag string) bool {
	for _, t := range foodTags {
		if t == tag {
			return true
		}
	}
	return false
}

func listFoodPlaces() []FoodPlace {
	out := []FoodPlace{}
	rows, err := database.DB.Query(`
SELECT p.id,p.user_id,p.shop_name,p.location,p.description,p.tag,p.image,p.created_at,
IFNULL((SELECT AVG(score) FROM food_ratings r WHERE r.place_id=p.id),0),
(SELECT COUNT(1) FROM food_ratings r WHERE r.place_id=p.id)
FROM food_places p WHERE p.status=1
ORDER BY (SELECT IFNULL(AVG(r.score),0) FROM food_ratings r WHERE r.place_id=p.id) DESC,
         (SELECT COUNT(1) FROM food_ratings r WHERE r.place_id=p.id) DESC, p.id DESC`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p FoodPlace
		if rows.Scan(&p.ID, &p.UserID, &p.ShopName, &p.Location, &p.Description,
			&p.Tag, &p.Image, &p.CreatedAt, &p.Avg, &p.RatingCount) == nil {
			out = append(out, p)
		}
	}
	return out
}

func loadFoodPlace(id, uid int64) *FoodPlace {
	p := &FoodPlace{}
	err := database.DB.QueryRow(`
SELECT p.id,p.user_id,p.shop_name,p.location,p.description,p.tag,p.image,p.created_at,
IFNULL((SELECT AVG(score) FROM food_ratings r WHERE r.place_id=p.id),0),
(SELECT COUNT(1) FROM food_ratings r WHERE r.place_id=p.id),
IFNULL((SELECT score FROM food_ratings r WHERE r.place_id=p.id AND r.user_id=?),0)
FROM food_places p WHERE p.id=? AND p.status=1`, uid, id,
	).Scan(&p.ID, &p.UserID, &p.ShopName, &p.Location, &p.Description,
		&p.Tag, &p.Image, &p.CreatedAt, &p.Avg, &p.RatingCount, &p.MyScore)
	if err != nil {
		return nil
	}
	return p
}

func listFoodReviews(placeID int64) []FoodReview {
	out := []FoodReview{}
	rows, err := database.DB.Query(`
SELECT r.id,r.user_id,
IFNULL(NULLIF(u.username,''), IFNULL(NULLIF(r.nickname,''),'匿名')),
IFNULL(u.avatar,''),r.content,IFNULL(u.exp,0),r.created_at
FROM food_reviews r LEFT JOIN users u ON u.id=r.user_id
WHERE r.place_id=? AND r.status=1 ORDER BY r.id DESC`, placeID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var r FoodReview
		var exp int
		if rows.Scan(&r.ID, &r.UserID, &r.Nickname, &r.Avatar, &r.Content, &exp, &r.CreatedAt) == nil {
			r.Level = models.LevelForExp(exp).Num
			out = append(out, r)
		}
	}
	return out
}

// FoodList 必吃榜列表
func FoodList(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	canFood := u != nil && models.LevelForExp(u.Exp).Num >= 2
	Render(w, r, "layout.html", "pg_food", map[string]any{
		"places":  listFoodPlaces(),
		"tags":    foodTags,
		"canFood": canFood,
		"err":     r.URL.Query().Get("err"),
	})
}

// FoodNewPage 添加店铺页（必须登录）
func FoodNewPage(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if u == nil {
		http.Redirect(w, r, "/login?next=/food/new", http.StatusFound)
		return
	}
	if models.LevelForExp(u.Exp).Num < 2 {
		http.Redirect(w, r, "/food?err="+url.QueryEscape("Lv2 起才能投稿必吃榜，继续获取经验解锁"), http.StatusSeeOther)
		return
	}
	Render(w, r, "layout.html", "pg_food_new", map[string]any{
		"tags": foodTags,
		"form": map[string]string{"tag": "正餐"},
	})
}

// FoodCreate 发布店铺（必须登录 + 验证码；店铺图片可选）
func FoodCreate(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if u == nil {
		http.Redirect(w, r, "/login?next=/food/new", http.StatusFound)
		return
	}
	if models.LevelForExp(u.Exp).Num < 2 {
		http.Redirect(w, r, "/food?err="+url.QueryEscape("Lv2 起才能投稿必吃榜，继续获取经验解锁"), http.StatusSeeOther)
		return
	}
	multipart := strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/")
	if multipart {
		r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			http.Error(w, "上传内容过大或表单无效", http.StatusBadRequest)
			return
		}
	} else {
		_ = r.ParseForm()
	}
	form := map[string]string{
		"shop_name":   strings.TrimSpace(r.FormValue("shop_name")),
		"location":    strings.TrimSpace(r.FormValue("location")),
		"description": strings.TrimSpace(r.FormValue("description")),
		"tag":         strings.TrimSpace(r.FormValue("tag")),
	}
	failForm := func(msg string) {
		Render(w, r, "layout.html", "pg_food_new", map[string]any{
			"tags": foodTags, "form": form, "err": msg,
		})
	}
	if !util.VerifyCaptcha(r.FormValue("captcha_id"), r.FormValue("captcha_code")) {
		failForm("验证码错误或已过期")
		return
	}
	shop := util.StripHTML(form["shop_name"])
	loc := util.StripHTML(form["location"])
	desc := util.StripHTML(form["description"])
	if n := len([]rune(shop)); n < 1 || n > 40 {
		failForm("店名需为 1-40 字")
		return
	}
	if n := len([]rune(loc)); n < 1 || n > 80 {
		failForm("位置需为 1-80 字")
		return
	}
	if n := len([]rune(desc)); n < 1 || n > 500 {
		failForm("描述需为 1-500 字")
		return
	}
	if !validFoodTag(form["tag"]) {
		failForm("请选择正确的标签")
		return
	}
	imagePath := ""
	if multipart {
		if f, fh, err := r.FormFile("image"); err == nil {
			defer f.Close()
			imagePath, err = util.SaveImage(fh)
			if err != nil {
				failForm("店铺图片：" + err.Error())
				return
			}
		} else if err != http.ErrMissingFile {
			failForm("店铺图片上传失败")
			return
		}
	}
	res, err := database.DB.Exec(
		"INSERT INTO food_places(user_id,shop_name,location,description,tag,image,status,created_at) VALUES(?,?,?,?,?,?,1,?)",
		u.ID, shop, loc, desc, form["tag"], imagePath, models.Now())
	if err != nil {
		if imagePath != "" {
			util.RemoveUpload(imagePath)
		}
		failForm("发布失败，请稍后重试")
		return
	}
	id, _ := res.LastInsertId()
	http.Redirect(w, r, fmt.Sprintf("/food/%d", id), http.StatusSeeOther)
}

// FoodDetail 店铺详情
func FoodDetail(w http.ResponseWriter, r *http.Request) {
	id := pathInt64(r, "id")
	u := CurrentUser(r)
	p := loadFoodPlace(id, userIDOf(u))
	if p == nil {
		http.NotFound(w, r)
		return
	}
	Render(w, r, "layout.html", "pg_food_detail", map[string]any{
		"place":   *p,
		"reviews": listFoodReviews(id),
		"err":     r.URL.Query().Get("err"),
	})
}

// FoodReviewSubmit 发表评价（必须登录）
func FoodReviewSubmit(w http.ResponseWriter, r *http.Request) {
	id := pathInt64(r, "id")
	u := CurrentUser(r)
	if u == nil {
		http.Redirect(w, r, fmt.Sprintf("/login?next=/food/%d", id), http.StatusFound)
		return
	}
	p := loadFoodPlace(id, u.ID)
	if p == nil {
		http.NotFound(w, r)
		return
	}
	content := util.StripHTML(r.FormValue("content"))
	if n := len([]rune(content)); n < 1 || n > 500 {
		http.Redirect(w, r, fmt.Sprintf("/food/%d?err=%s", id, url.QueryEscape("评价需为 1-500 字")), http.StatusSeeOther)
		return
	}
	if _, err := database.DB.Exec(
		"INSERT INTO food_reviews(place_id,user_id,content,status,created_at) VALUES(?,?,?,1,?)",
		id, u.ID, content, models.Now()); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/food/%d?err=%s", id, url.QueryEscape("评价失败，请稍后重试")), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/food/%d#reviews", id), http.StatusSeeOther)
}

// FoodRate 评分接口：同一用户可改分，返回最新平均分。
func FoodRate(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if u == nil {
		fail(w, http.StatusUnauthorized, "请先登录后评分")
		return
	}
	id := pathInt64(r, "id")
	if loadFoodPlace(id, u.ID) == nil {
		fail(w, http.StatusNotFound, "店铺不存在")
		return
	}
	score := 0
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var req struct {
			Score int `json:"score"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, "参数格式错误")
			return
		}
		score = req.Score
	} else {
		score, _ = strconv.Atoi(r.FormValue("score"))
	}
	if score < 1 || score > 5 {
		fail(w, http.StatusBadRequest, "评分需为 1-5 星")
		return
	}
	q := `INSERT INTO food_ratings(place_id,user_id,score,created_at,updated_at) VALUES(?,?,?,?,?)
ON CONFLICT(place_id,user_id) DO UPDATE SET score=excluded.score, updated_at=excluded.updated_at`
	if database.Driver == "mysql" {
		q = `INSERT INTO food_ratings(place_id,user_id,score,created_at,updated_at) VALUES(?,?,?,?,?)
ON DUPLICATE KEY UPDATE score=VALUES(score), updated_at=VALUES(updated_at)`
	}
	now := models.Now()
	if _, err := database.DB.Exec(q, id, u.ID, score, now, now); err != nil {
		fail(w, http.StatusInternalServerError, "评分失败，请稍后重试")
		return
	}
	var avg float64
	var count int
	_ = database.DB.QueryRow(
		"SELECT IFNULL(AVG(score),0),COUNT(1) FROM food_ratings WHERE place_id=?", id,
	).Scan(&avg, &count)
	okJSON(w, map[string]any{"avg": avg, "count": count, "my": score})
}
