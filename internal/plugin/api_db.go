// Host API：数据层（clv.db.* / clv.table）。
//
// 安全策略：
//   - 声明式权限 db 是使用数据能力的前提
//   - 写操作默认只允许本插件前缀（pl_<slug>_）的表；内核表只读
//   - 声明 db.admin 权限可放开全库写（需管理员在后台显式授权）
//   - 查询行数设上限，防止插件拉爆内存
package plugin

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/dop251/goja"

	"clearlove/internal/database"
)

const maxQueryRows = 1000

type sqlQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

type sqlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func (rt *jsRuntime) installDB(clv *goja.Object) error {
	db := rt.vm.NewObject()

	_ = db.Set("dialect", rt.jsFn(func(call goja.FunctionCall) (any, error) {
		return database.Driver, nil
	}))

	_ = db.Set("table", rt.jsFn(func(call goja.FunctionCall) (any, error) {
		args := argsOf(call)
		if len(args) == 0 {
			return nil, errors.New("clv.db.table(name) 需要一个参数")
		}
		return rt.plugin.TableName(strOf(args[0])), nil
	}))

	_ = db.Set("query", rt.jsFn(rt.dbQuery))
	_ = db.Set("get", rt.jsFn(rt.dbGet))
	_ = db.Set("exec", rt.jsFn(rt.dbExec))
	_ = db.Set("tx", rt.jsFn(rt.dbTx))
	_ = clv.Set("db", db)

	// 建表：clv.table(name, columns, opts)
	_ = clv.Set("table", rt.jsFn(rt.createTable))
	return nil
}

// ---------- SQL 安全 ----------

var (
	reTableRef     = regexp.MustCompile("(?i)\\b(?:into|update|from|table|join)\\s+[`\"']?([a-zA-Z0-9_]+)")
	reSQLComment   = regexp.MustCompile(`(?s)/\*.*?\*/|--[^\n]*`)
	reDangerousSQL = regexp.MustCompile(`(?i)\b(pragma|attach|detach|vacuum)\b`)
)

// sqlAllowed 插件 SQL 管控：
//   - 声明 db.admin 权限的插件不受限
//   - 拒绝多语句（分号拆分逐段校验）与 PRAGMA/ATTACH/DETACH/VACUUM 等危险语句
//   - 读写一致：所有被引用的表必须落在本插件前缀（pl_<slug>_）内；
//     读取内核表请走 clv.site（site.read 权限），防止仅声明 db 的插件拖库
func (rt *jsRuntime) sqlAllowed(sqlText string) bool {
	if rt.app != nil && rt.app.Plugin.HasPermission("db.admin") {
		return true
	}
	// 去除注释再校验，防止 UPDATE/**/settings 这类拆词绕过
	cleaned := reSQLComment.ReplaceAllString(sqlText, " ")
	for _, seg := range strings.Split(cleaned, ";") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if reDangerousSQL.MatchString(seg) {
			return false
		}
		prefix := strings.ToLower(rt.plugin.TablePrefix())
		for _, m := range reTableRef.FindAllStringSubmatch(seg, -1) {
			if !strings.HasPrefix(strings.ToLower(m[1]), prefix) {
				return false
			}
		}
	}
	return true
}

func (rt *jsRuntime) sqlDeniedErr(sqlText string) error {
	return fmt.Errorf("插件 %s 只能访问自己的数据表（%s*）；读取内核数据请用 clv.site，如需访问其它表请声明 db.admin 权限。SQL: %.100s",
		rt.plugin.SlugOf(), rt.plugin.TablePrefix(), strings.TrimSpace(sqlText))
}

// ---------- db.query / db.get / db.exec ----------

func (rt *jsRuntime) dbQuery(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("db"); err != nil {
		return nil, err
	}
	args := argsOf(call)
	if len(args) == 0 || strOf(args[0]) == "" {
		return nil, errors.New("clv.db.query(sql, ...args) 需要 SQL")
	}
	sqlText := strOf(args[0])
	if !rt.sqlAllowed(sqlText) {
		return nil, rt.sqlDeniedErr(sqlText)
	}
	if database.DB == nil {
		return nil, errors.New("数据库未连接")
	}
	return queryMaps(database.DB, sqlText, args[1:])
}

func (rt *jsRuntime) dbGet(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("db"); err != nil {
		return nil, err
	}
	args := argsOf(call)
	if len(args) == 0 || strOf(args[0]) == "" {
		return nil, errors.New("clv.db.get(sql, ...args) 需要 SQL")
	}
	sqlText := strOf(args[0])
	if !rt.sqlAllowed(sqlText) {
		return nil, rt.sqlDeniedErr(sqlText)
	}
	if database.DB == nil {
		return nil, errors.New("数据库未连接")
	}
	rows, err := queryMaps(database.DB, sqlText, args[1:])
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

func (rt *jsRuntime) dbExec(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("db"); err != nil {
		return nil, err
	}
	args := argsOf(call)
	if len(args) == 0 || strOf(args[0]) == "" {
		return nil, errors.New("clv.db.exec(sql, ...args) 需要 SQL")
	}
	sqlText := strOf(args[0])
	if !rt.sqlAllowed(sqlText) {
		return nil, rt.sqlDeniedErr(sqlText)
	}
	if database.DB == nil {
		return nil, errors.New("数据库未连接")
	}
	return execResult(database.DB, sqlText, args[1:])
}

// dbTx 事务：clv.db.tx(function (tx) { tx.exec(...); tx.get(...); })
func (rt *jsRuntime) dbTx(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("db"); err != nil {
		return nil, err
	}
	fn, ok := goja.AssertFunction(call.Argument(0))
	if !ok {
		return nil, errors.New("clv.db.tx(fn) 需要传入函数")
	}
	if database.DB == nil {
		return nil, errors.New("数据库未连接")
	}
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("开启事务失败: %w", err)
	}
	txObj := rt.vm.NewObject()
	_ = txObj.Set("exec", rt.jsFn(func(c goja.FunctionCall) (any, error) {
		a := argsOf(c)
		if len(a) == 0 {
			return nil, errors.New("tx.exec(sql, ...args) 需要 SQL")
		}
		s := strOf(a[0])
		if !rt.sqlAllowed(s) {
			return nil, rt.sqlDeniedErr(s)
		}
		return execResult(tx, s, a[1:])
	}))
	_ = txObj.Set("get", rt.jsFn(func(c goja.FunctionCall) (any, error) {
		a := argsOf(c)
		if len(a) == 0 {
			return nil, errors.New("tx.get(sql, ...args) 需要 SQL")
		}
		s := strOf(a[0])
		if !rt.sqlAllowed(s) {
			return nil, rt.sqlDeniedErr(s)
		}
		rows, err := queryMaps(tx, s, a[1:])
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, nil
		}
		return rows[0], nil
	}))
	_ = txObj.Set("query", rt.jsFn(func(c goja.FunctionCall) (any, error) {
		a := argsOf(c)
		if len(a) == 0 {
			return nil, errors.New("tx.query(sql, ...args) 需要 SQL")
		}
		s := strOf(a[0])
		if !rt.sqlAllowed(s) {
			return nil, rt.sqlDeniedErr(s)
		}
		return queryMaps(tx, s, a[1:])
	}))

	v, err := fn(goja.Undefined(), txObj)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交事务失败: %w", err)
	}
	return exportJS(v), nil
}

// ---------- 建表 ----------

// createTable clv.table(name, columns, opts) —— 幂等建表
func (rt *jsRuntime) createTable(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("db"); err != nil {
		return nil, err
	}
	if database.DB == nil {
		return nil, errors.New("数据库未连接")
	}
	name := NormalizeSlug(strOf(call.Argument(0)))
	if name == "" {
		return nil, errors.New("clv.table(name, columns, opts) 需要表名（字母/数字/下划线）")
	}
	cols := mapOfAny(call.Argument(1))
	if len(cols) == 0 {
		return nil, errors.New("clv.table 需要列定义对象，例如 { user_id: 'int', day: 'string' }")
	}
	opts := mapOfAny(call.Argument(2))

	table := rt.plugin.TableName(name)
	ddl := buildCreateTable(database.Driver, table, cols, opts)
	if _, err := database.DB.Exec(ddl); err != nil {
		return nil, fmt.Errorf("建表 %s 失败: %w", table, err)
	}
	for _, ix := range buildIndexes(table, opts) {
		_, _ = database.DB.Exec(ix) // MySQL 不支持 IF NOT EXISTS，重复创建的错误忽略
	}
	return table, nil
}

func sqlType(driver, t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if driver == "mysql" {
		switch t {
		case "int":
			return "INT"
		case "bigint":
			return "BIGINT"
		case "float", "number":
			return "DOUBLE"
		case "bool":
			return "TINYINT"
		case "text":
			return "MEDIUMTEXT"
		case "json":
			return "MEDIUMTEXT"
		case "time":
			return "VARCHAR(40)"
		default:
			return "VARCHAR(255)"
		}
	}
	switch t {
	case "int", "bigint", "bool":
		return "INTEGER"
	case "float", "number":
		return "REAL"
	case "time":
		return "TEXT"
	default:
		return "TEXT"
	}
}

func buildCreateTable(driver, table string, cols map[string]any, opts map[string]any) string {
	var b strings.Builder
	b.WriteString("CREATE TABLE IF NOT EXISTS " + table + " (")
	if driver == "mysql" {
		b.WriteString("id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT")
	} else {
		b.WriteString("id INTEGER PRIMARY KEY AUTOINCREMENT")
	}
	names := make([]string, 0, len(cols))
	for k := range cols {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		col := strings.ReplaceAll(NormalizeSlug(k), "-", "_")
		if col == "" || col == "id" {
			continue
		}
		b.WriteString(", " + col + " " + sqlType(driver, strOf(cols[k])))
	}
	for _, u := range listOfLists(opts["unique"]) {
		b.WriteString(", UNIQUE(" + strings.Join(u, ",") + ")")
	}
	b.WriteString(")")
	return b.String()
}

func buildIndexes(table string, opts map[string]any) []string {
	var out []string
	for i, cols := range listOfLists(opts["indexes"]) {
		out = append(out, fmt.Sprintf("CREATE INDEX idx_%s_%d ON %s (%s)",
			table, i, table, strings.Join(cols, ",")))
	}
	return out
}

// ---------- 通用查询辅助 ----------

func queryMaps(q sqlQuerier, sqlText string, args []any) ([]map[string]any, error) {
	rows, err := q.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, 16)
	for rows.Next() {
		if len(out) >= maxQueryRows {
			return nil, fmt.Errorf("查询结果超过 %d 行上限，请加 LIMIT", maxQueryRows)
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			m[c] = normalizeSQLValue(vals[i])
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func execResult(e sqlExecer, sqlText string, args []any) (map[string]any, error) {
	res, err := e.Exec(sqlText, args...)
	if err != nil {
		return nil, err
	}
	lastID, _ := res.LastInsertId()
	affected, _ := res.RowsAffected()
	return map[string]any{"lastId": lastID, "rowsAffected": affected}, nil
}

// normalizeSQLValue 统一驱动差异（[]byte -> string 等）
func normalizeSQLValue(v any) any {
	switch t := v.(type) {
	case []byte:
		return string(t)
	default:
		return v
	}
}

// mapOfAny 取 goja 对象参数为 map
func mapOfAny(v goja.Value) map[string]any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	if m, ok := v.Export().(map[string]any); ok {
		return m
	}
	return nil
}

// listOfLists 解析 [["a","b"],["c"]] 结构
func listOfLists(v any) [][]string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([][]string, 0, len(arr))
	for _, item := range arr {
		inner, ok := item.([]any)
		if !ok {
			continue
		}
		row := make([]string, 0, len(inner))
		for _, c := range inner {
			if col := strings.ReplaceAll(NormalizeSlug(strOf(c)), "-", "_"); col != "" {
				row = append(row, col)
			}
		}
		if len(row) > 0 {
			out = append(out, row)
		}
	}
	return out
}
