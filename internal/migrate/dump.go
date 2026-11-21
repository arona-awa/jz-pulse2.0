package migrate

import (
	"errors"
	"strings"
)

var errEmptyFile = errors.New("文件内容为空")

// dumpSource MySQL 导出文件（.sql）数据源。
// tables 保存的是**有序**列名列表：部分 INSERT 不带列名列表，
// 必须按建表语句的列顺序还原，否则字段会错位。
type dumpSource struct {
	tables map[string][]string
	rows   map[string][]Row
}

func (s *dumpSource) HasTable(table string) bool { _, ok := s.tables[table]; return ok }

func (s *dumpSource) Kind() string { return "mysqldump" }

func (s *dumpSource) Close() error { return nil }

func (s *dumpSource) Rows(table string, _ ...string) ([]Row, error) {
	return s.rows[table], nil
}

// parseDump 解析 MySQL 导出文件
func parseDump(data []byte) (Source, error) {
	s := &dumpSource{tables: map[string][]string{}, rows: map[string][]Row{}}
	stmts := splitSQL(string(data))
	for _, stmt := range stmts {
		up := strings.ToUpper(strings.TrimSpace(stmt))
		switch {
		case strings.HasPrefix(up, "CREATE TABLE"):
			name, cols := parseCreateTable(stmt)
			if name == "" {
				continue
			}
			s.tables[name] = cols
		case strings.HasPrefix(up, "INSERT INTO"):
			name, cols, tuples := parseInsert(stmt)
			if name == "" || len(tuples) == 0 {
				continue
			}
			if len(cols) == 0 {
				// 没有列名列表时按建表语句的列顺序还原
				cols = s.tables[name]
				if len(cols) == 0 {
					continue
				}
			}
			if _, ok := s.tables[name]; !ok {
				s.tables[name] = cols
			}
			for _, t := range tuples {
				row := Row{}
				for i, c := range cols {
					if i < len(t) {
						row[c] = t[i]
					}
				}
				s.rows[name] = append(s.rows[name], row)
			}
		}
	}
	if len(s.tables) == 0 {
		return nil, errors.New("未能从文件中解析出任何数据表，请确认这是 MySQL 导出的 .sql 文件")
	}
	return s, nil
}

// ---------- SQL 语句切分 ----------

// splitSQL 按分号切分 SQL 语句，自动跳过字符串、标识符与注释中的分号
func splitSQL(s string) []string {
	var out []string
	var cur strings.Builder
	n := len(s)
	for i := 0; i < n; i++ {
		c := s[i]
		switch c {
		case '\'', '"', '`':
			// 引用内容整体拷贝（含转义）
			q := c
			cur.WriteByte(c)
			i++
			for i < n {
				if s[i] == '\\' && i+1 < n && q != '`' {
					cur.WriteByte(s[i])
					cur.WriteByte(s[i+1])
					i += 2
					continue
				}
				cur.WriteByte(s[i])
				if s[i] == q {
					i++
					break
				}
				i++
			}
			i--
		case '-':
			// -- 行注释
			if i+1 < n && s[i+1] == '-' {
				for i < n && s[i] != '\n' {
					i++
				}
			} else {
				cur.WriteByte(c)
			}
		case '#':
			for i < n && s[i] != '\n' {
				i++
			}
		case '/':
			// 块注释（含 /*! 条件注释）
			if i+1 < n && s[i+1] == '*' {
				i += 2
				for i+1 < n && !(s[i] == '*' && s[i+1] == '/') {
					i++
				}
				i++
			} else {
				cur.WriteByte(c)
			}
		case ';':
			if stmt := strings.TrimSpace(cur.String()); stmt != "" {
				out = append(out, stmt)
			}
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if stmt := strings.TrimSpace(cur.String()); stmt != "" {
		out = append(out, stmt)
	}
	return out
}

// ---------- CREATE TABLE ----------

// parseCreateTable 解析建表语句，返回表名与列名列表
func parseCreateTable(stmt string) (string, []string) {
	up := strings.ToUpper(stmt)
	i := strings.Index(up, "CREATE TABLE")
	if i < 0 {
		return "", nil
	}
	i += len("CREATE TABLE")
	// 跳过 IF NOT EXISTS
	rest := strings.ToUpper(stmt[i:])
	if j := strings.Index(rest, "IF NOT EXISTS"); j >= 0 && j < 24 {
		i += j + len("IF NOT EXISTS")
	}
	i = skipSpace(stmt, i)
	name, i := readIdent(stmt, i)
	i = skipSpace(stmt, i)
	if name == "" || i >= len(stmt) || stmt[i] != '(' {
		return name, nil
	}
	end := matchParen(stmt, i)
	if end < 0 {
		return name, nil
	}
	var cols []string
	for _, part := range splitTopLevel(stmt[i+1 : end]) {
		part = strings.TrimSpace(part)
		// 只取以反引号开头的列定义，跳过 PRIMARY KEY / KEY / UNIQUE KEY / CONSTRAINT 等
		if !strings.HasPrefix(part, "`") {
			continue
		}
		if c, _ := readIdent(part, 0); c != "" {
			cols = append(cols, c)
		}
	}
	return name, cols
}

// ---------- INSERT INTO ----------

// parseInsert 解析插入语句，返回表名、列名列表与值元组
func parseInsert(stmt string) (string, []string, [][]string) {
	up := strings.ToUpper(stmt)
	i := strings.Index(up, "INSERT INTO")
	if i < 0 {
		return "", nil, nil
	}
	i += len("INSERT INTO")
	i = skipSpace(stmt, i)
	// 可选修饰符
	for _, kw := range []string{"LOW_PRIORITY", "DELAYED", "HIGH_PRIORITY", "IGNORE"} {
		if strings.HasPrefix(strings.ToUpper(stmt[i:]), kw) {
			i += len(kw)
			i = skipSpace(stmt, i)
		}
	}
	table, i := readIdent(stmt, i)
	if table == "" {
		return "", nil, nil
	}
	i = skipSpace(stmt, i)
	var cols []string
	if i < len(stmt) && stmt[i] == '(' {
		end := matchParen(stmt, i)
		if end < 0 {
			return table, nil, nil
		}
		for _, c := range strings.Split(stmt[i+1:end], ",") {
			c = strings.TrimSpace(c)
			c = strings.Trim(c, "`")
			if c != "" {
				cols = append(cols, c)
			}
		}
		i = end + 1
	}
	rest := stmt[i:]
	vi := strings.Index(strings.ToUpper(rest), "VALUES")
	if vi < 0 {
		return table, cols, nil
	}
	return table, cols, parseTuples(rest[vi+len("VALUES"):])
}

// ---------- 词法辅助 ----------

func skipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

// readIdent 读取标识符（支持反引号包裹）
func readIdent(s string, i int) (string, int) {
	i = skipSpace(s, i)
	if i >= len(s) {
		return "", i
	}
	if s[i] == '`' {
		i++
		var b strings.Builder
		for i < len(s) {
			if s[i] == '`' {
				if i+1 < len(s) && s[i+1] == '`' {
					b.WriteByte('`')
					i += 2
					continue
				}
				i++
				break
			}
			b.WriteByte(s[i])
			i++
		}
		return b.String(), i
	}
	start := i
	for i < len(s) && !strings.ContainsRune(" \t\n\r(,;", rune(s[i])) {
		i++
	}
	return s[start:i], i
}

// matchParen 返回与 s[i]（'('）匹配的 ')' 下标，找不到返回 -1
func matchParen(s string, i int) int {
	depth := 0
	for ; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		case '\'', '"', '`':
			q := s[i]
			i++
			for i < len(s) {
				if s[i] == '\\' && i+1 < len(s) && q != '`' {
					i += 2
					continue
				}
				if s[i] == q {
					break
				}
				i++
			}
		}
	}
	return -1
}

// splitTopLevel 按顶层逗号切分（忽略括号与引号内的逗号）
func splitTopLevel(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case '\'', '"', '`':
			q := s[i]
			i++
			for i < len(s) {
				if s[i] == '\\' && i+1 < len(s) && q != '`' {
					i += 2
					continue
				}
				if s[i] == q {
					break
				}
				i++
			}
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

// parseTuples 解析 VALUES 后的 "(v1,v2),(v3,v4)" 列表
func parseTuples(s string) [][]string {
	var out [][]string
	i, n := 0, len(s)
	for i < n {
		for i < n && s[i] != '(' {
			i++
		}
		if i >= n {
			break
		}
		i++ // 跳过 '('
		var vals []string
		for {
			v, ni := parseValue(s, i)
			vals = append(vals, v)
			i = skipSpace(s, ni)
			if i < n && s[i] == ',' {
				i++
				continue
			}
			if i < n && s[i] == ')' {
				i++
			}
			break
		}
		out = append(out, vals)
	}
	return out
}

// parseValue 解析一个值，返回字符串值与新的下标（NULL 与空串统一为空串）
func parseValue(s string, i int) (string, int) {
	n := len(s)
	i = skipSpace(s, i)
	if i >= n {
		return "", i
	}
	if s[i] == '\'' || s[i] == '"' {
		q := s[i]
		i++
		var b strings.Builder
		for i < n {
			c := s[i]
			if c == '\\' && i+1 < n {
				b.WriteByte(decodeEscape(s[i+1]))
				i += 2
				continue
			}
			if c == q {
				// '' 表示一个引号字符
				if i+1 < n && s[i+1] == q {
					b.WriteByte(q)
					i += 2
					continue
				}
				i++
				break
			}
			b.WriteByte(c)
			i++
		}
		return b.String(), i
	}
	start := i
	for i < n && s[i] != ',' && s[i] != ')' {
		i++
	}
	raw := strings.TrimSpace(s[start:i])
	if strings.EqualFold(raw, "NULL") {
		return "", i
	}
	return raw, i
}

// decodeEscape 解析 MySQL 反斜杠转义
func decodeEscape(c byte) byte {
	switch c {
	case 'n':
		return '\n'
	case 'r':
		return '\r'
	case 't':
		return '\t'
	case '0':
		return 0
	case 'b':
		return '\b'
	case 'Z':
		return 26
	default:
		return c // \\ \' \" \% \_ 等保持原字符
	}
}
