package sqlrepo

import (
	"fmt"
	"regexp"

	"github.com/woo721/cursor_test/internal/domain"
)

// Statement 是一次只读聚合查询的 SQL 文本与绑定参数。
type Statement struct {
	SQL  string
	Args []any
}

// Builder 按方言生成零售/家装漏斗固定形状的 SQL，不接受任意条件片段。
type Builder interface {
	Retail(domain.Query) Statement
	Renovation(domain.Query) Statement
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// hiveLiteralSafe 与领域已校验的 org_id / YYYY-MM-DD 字符集对齐：仅字母数字、下划线、连字符。
func hiveLiteralSafe(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}

// quoteHiveLiteral 在嵌入 Hive SQL 前二次校验字面量字节集并加单引号。
// HiveServer2 不支持查询参数绑定，因此只能嵌入领域层已校验的 org/日期；此处拒绝单引号等注入字符。
func quoteHiveLiteral(s string) (string, error) {
	if !hiveLiteralSafe(s) {
		return "", domain.ErrInvalidArgument
	}
	return "'" + s + "'", nil
}

func validateIdentifiers(ids ...string) error {
	for _, id := range ids {
		if !identifierPattern.MatchString(id) {
			return domain.ErrInvalidArgument
		}
	}
	return nil
}

func qualify(prefix, table string) string {
	return prefix + "." + table
}

const retailSelect = `SELECT
  COALESCE(SUM(order_count), 0),
  CAST(COALESCE(SUM(sales_amount), 0) AS DECIMAL(38, 2))
FROM %s
WHERE org_id = %s AND stat_date >= %s AND stat_date <= %s`

const renovationSelect = `SELECT
  COALESCE(SUM(lead_count), 0),
  COALESCE(SUM(invited_count), 0),
  COALESCE(SUM(measured_count), 0),
  COALESCE(SUM(signed_count), 0),
  COALESCE(SUM(started_count), 0),
  COALESCE(SUM(completed_count), 0)
FROM %s
WHERE org_id = %s AND stat_date >= %s AND stat_date <= %s`

// boundBuilder 面向 StarRocks/Trino：标识符来自启动配置校验，过滤值用 ? 绑定，避免拼接。
type boundBuilder struct {
	retailTable     string
	renovationTable string
}

// NewBoundBuilder 校验库/表标识符后构造绑定参数方言的 SQL 构建器。
func NewBoundBuilder(prefix, retailTable, renovationTable string) (Builder, error) {
	if err := validateIdentifiers(prefix, retailTable, renovationTable); err != nil {
		return nil, err
	}
	return &boundBuilder{
		retailTable:     qualify(prefix, retailTable),
		renovationTable: qualify(prefix, renovationTable),
	}, nil
}

func (b *boundBuilder) Retail(q domain.Query) Statement {
	sql := fmt.Sprintf(retailSelect, b.retailTable, "?", "?", "?")
	return Statement{
		SQL:  sql,
		Args: []any{q.OrgID, q.Range.StartString(), q.Range.EndString()},
	}
}

func (b *boundBuilder) Renovation(q domain.Query) Statement {
	sql := fmt.Sprintf(renovationSelect, b.renovationTable, "?", "?", "?")
	return Statement{
		SQL:  sql,
		Args: []any{q.OrgID, q.Range.StartString(), q.Range.EndString()},
	}
}

// hiveBuilder 面向 HiveServer2：无 Args，仅嵌入领域已校验的 org/日期字面量。
type hiveBuilder struct {
	retailTable     string
	renovationTable string
}

// NewHiveBuilder 校验库/表标识符后构造 Hive 字面量嵌入方言的 SQL 构建器。
func NewHiveBuilder(prefix, retailTable, renovationTable string) (Builder, error) {
	if err := validateIdentifiers(prefix, retailTable, renovationTable); err != nil {
		return nil, err
	}
	return &hiveBuilder{
		retailTable:     qualify(prefix, retailTable),
		renovationTable: qualify(prefix, renovationTable),
	}, nil
}

func mustQuoteHive(s string) string {
	quoted, err := quoteHiveLiteral(s)
	if err != nil {
		// 领域层应已校验 org_id 与日期；此处失败表示不变量被破坏。
		panic("sqlrepo: unsafe hive literal after domain validation")
	}
	return quoted
}

func (b *hiveBuilder) Retail(q domain.Query) Statement {
	sql := fmt.Sprintf(
		retailSelect,
		b.retailTable,
		mustQuoteHive(q.OrgID),
		mustQuoteHive(q.Range.StartString()),
		mustQuoteHive(q.Range.EndString()),
	)
	return Statement{SQL: sql, Args: nil}
}

func (b *hiveBuilder) Renovation(q domain.Query) Statement {
	sql := fmt.Sprintf(
		renovationSelect,
		b.renovationTable,
		mustQuoteHive(q.OrgID),
		mustQuoteHive(q.Range.StartString()),
		mustQuoteHive(q.Range.EndString()),
	)
	return Statement{SQL: sql, Args: nil}
}
