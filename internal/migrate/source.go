package migrate

import (
	"database/sql"
	"os"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

// Row 一行数据：列名 -> 字符串值（NULL 统一映射为空串）
type Row map[string]string

// Source 旧版数据源。支持两种来源：
//   - SQLite 数据库文件（旧版 data.db）
//   - MySQL 导出文件（mysqldump / 宝塔导出的 .sql）
type Source interface {
	// HasTable 数据源中是否存在指定表
	HasTable(table string) bool
	// Rows 读取整表，fields 为候选列名（数据源中不存在的列不会出现在结果行里）
	Rows(table string, fields ...string) ([]Row, error)
	// Kind 数据源类型（sqlite / mysqldump）
	Kind() string
	// Close 释放资源
	Close() error
}

// field 从行中按候选列名取第一个非空值
func field(row Row, names ...string) string {
	for _, n := range names {
		if v, ok := row[n]; ok && v != "" {
			return v
		}
	}
	return ""
}

// fieldInt 取整数值（缺失或非法时返回 0）
func fieldInt(row Row, names ...string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(field(row, names...)), 10, 64)
	return n
}

// ---------- SQLite 数据源 ----------

type sqliteSource struct {
	db   *sql.DB
	has  map[string]bool
	cols map[string]map[string]bool
}

// openSQLiteSource 打开旧版 SQLite 数据库文件
func openSQLiteSource(path string) (*sqliteSource, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	s := &sqliteSource{db: db, has: map[string]bool{}, cols: map[string]map[string]bool{}}
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		db.Close()
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var name sql.NullString
		if rows.Scan(&name) == nil && name.Valid {
			tables = append(tables, name.String)
		}
	}
	rows.Close()
	for _, t := range tables {
		s.has[t] = true
		s.cols[t] = map[string]bool{}
		cr, err := db.Query("PRAGMA table_info(" + quoteIdent(t) + ")")
		if err != nil {
			continue
		}
		for cr.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt sql.NullString
			if cr.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk) == nil {
				s.cols[t][name] = true
			}
		}
		cr.Close()
	}
	return s, nil
}

func (s *sqliteSource) HasTable(table string) bool { return s.has[table] }

func (s *sqliteSource) Kind() string { return "sqlite" }

func (s *sqliteSource) Close() error { return s.db.Close() }

func (s *sqliteSource) Rows(table string, fields ...string) ([]Row, error) {
	if !s.has[table] {
		return nil, nil
	}
	var exprs []string
	for _, f := range fields {
		if s.cols[table][f] {
			exprs = append(exprs, quoteIdent(f)+" AS "+quoteIdent(f))
		}
	}
	if len(exprs) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query("SELECT " + strings.Join(exprs, ",") + " FROM " + quoteIdent(table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []Row
	for rows.Next() {
		vals := make([]sql.NullString, len(names))
		ptrs := make([]any, len(names))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		row := Row{}
		for i, c := range names {
			if vals[i].Valid {
				row[c] = vals[i].String
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// quoteIdent 用双引号包裹标识符（SQLite 兼容）
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// ---------- 数据源探测 ----------

// OpenSource 按文件内容自动识别数据源类型：SQLite 数据库文件或 MySQL 导出文件
func OpenSource(path string) (Source, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	head := make([]byte, 16)
	n, _ := f.Read(head)
	f.Close()
	// SQLite 文件头："SQLite format 3\x00"
	if n >= 16 && string(head[:15]) == "SQLite format 3" {
		return openSQLiteSource(path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errEmptyFile
	}
	return parseDump(data)
}
