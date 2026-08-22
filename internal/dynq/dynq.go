// Package dynq 是 sqlc_gen_dynamic 插件的运行时库(手写,不生成)。
// 协议是表无关的,协议机器也表无关:信封解码、类型收敛、游标编解码、
// 排序/seek 构造、运行期能力投影与前端 schema 全部收敛于此,
// 生成代码只保留表相关数据与穷举 switch。
package dynq

// FieldName 是逻辑字段名(camelCase)。协议与 typed 门只接受 camelCase 拼写,
// snake_case 物理列名不是任何输入面的合法拼写。
type FieldName string

// DataType 是生成期固化的字段逻辑类型,决定操作符能力矩阵与类型收敛路径。
type DataType string

const (
	DataTypeInteger  DataType = "integer"
	DataTypeNumber   DataType = "number"
	DataTypeDatetime DataType = "datetime"
	DataTypeDate     DataType = "date"
	DataTypeString   DataType = "string"
	DataTypeBoolean  DataType = "boolean"
	DataTypeUUID     DataType = "uuid"
	DataTypeEnum     DataType = "enum"
	DataTypeJSONB    DataType = "jsonb"
	DataTypeIntArray DataType = "int_array"
)

// Op 是 where 操作符。is_null / not_null 完全独立:不入预设、不因可空自动授予、
// 不从 eq/neq 推导。
type Op string

const (
	OpEq       Op = "eq"
	OpNeq      Op = "neq"
	OpGt       Op = "gt"
	OpGte      Op = "gte"
	OpLt       Op = "lt"
	OpLte      Op = "lte"
	OpBetween  Op = "between"
	OpIn       Op = "in"
	OpNotIn    Op = "not_in"
	OpPrefix   Op = "prefix"
	OpSuffix   Op = "suffix"
	OpContains Op = "contains"
	OpIsNull   Op = "is_null"
	OpNotNull  Op = "not_null"
	OpHas      Op = "has"
)

var allOps = map[Op]struct{}{
	OpEq: {}, OpNeq: {}, OpGt: {}, OpGte: {}, OpLt: {}, OpLte: {},
	OpBetween: {}, OpIn: {}, OpNotIn: {}, OpPrefix: {}, OpSuffix: {},
	OpContains: {}, OpIsNull: {}, OpNotNull: {}, OpHas: {},
}

// ParseOp 归一化操作符拼写,未知返回错误。
func ParseOp(s string) (Op, error) {
	op := Op(s)
	if _, ok := allOps[op]; !ok {
		return "", NewErr("", "", "unknown op: "+s, -1)
	}
	return op, nil
}

// OrderBy 是排序元素:逻辑字段名 + 方向。可用生成字段常量(如 UserField.CreatedAt)。
// json tag 面向前端 schema 的 default_sort 契约(全小写键)。
type OrderBy struct {
	Field FieldName `json:"field"`
	Desc  bool      `json:"desc"` // false = ASC
}

// FieldMeta 是单个字段的生成期元数据,同时驱动解析白名单、禁用校验与 schema 输出
// (三投影同源,物理上不可能漂移)。
type FieldMeta struct {
	Key        FieldName // 逻辑键 camelCase
	Column     string    // 物理列名 snake_case,仅生成物内部使用
	Label      string    // 展示文本,取自注释非指令部分
	DataType   DataType
	Nullable   bool
	Ops        []Op // where 能力(生成期白名单,运行期只做减法)
	Sortable   bool
	Selectable bool // 纯 UI 提示:服务端 fields 点名白名单 = 全部非 @hidden 列
	Hidden     bool // @hidden 全能力排除
	Enum       []string
	// Cast 是 seek 谓词占位符的显式 cast 后缀(如 "::uuid"),用于文本形态参数
	// 与 uuid/enum 列的空值安全等值比较;空 = 无需 cast。
	Cast string
	// API 非 nil 时,该字段 where 值控件的选项数据源为远程接口(@api 声明)。
	// 仅影响前端 schema 输出(选项值动态获取),不参与任何服务端校验路径。
	API *FieldAPI
}

// HasOp 判断字段是否声明了该操作符。
func (f FieldMeta) HasOp(op Op) bool {
	for _, o := range f.Ops {
		if o == op {
			return true
		}
	}
	return false
}

// TableMeta 是一张表的生成期元数据(registry)。
type TableMeta struct {
	Resource    string // 复数资源名,如 "users"
	Table       string // 物理表名
	Fields      []FieldMeta
	DefaultSort []OrderBy // 表级 @default_sort,已生成期校验
	Tiebreaker  FieldName // 主键列(@pk 声明),无条件追加为末位排序
	PageDefault int       // 默认 page_size
	PageMax     int       // page_size 上限
}

// Field 按逻辑键索引;ok=false 表示未知字段。
func (m TableMeta) Field(key FieldName) (FieldMeta, bool) {
	for _, f := range m.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return FieldMeta{}, false
}

// TiebreakerField 返回主键列元数据。
func (m TableMeta) TiebreakerField() FieldMeta {
	f, _ := m.Field(m.Tiebreaker)
	return f
}

// Result 是 List 查询函数的返回信封。
type Result[T any] struct {
	Items      []T
	HasMore    bool
	NextCursor string
}

// ListResponse 是 handler 层的列表响应信封:
// { items, total, page, pageSize, nextCursor, hasMore }(JSON 协议面统一小驼峰)。
// 游标模式 page 为 null;includeTotal=false 时 total 为 null。
type ListResponse[T any] struct {
	Items      []T    `json:"items"`
	Total      *int64 `json:"total"`
	Page       *int   `json:"page"`
	PageSize   int    `json:"pageSize"`
	NextCursor string `json:"nextCursor"`
	HasMore    bool   `json:"hasMore"`
}

// NewListResponse 组装响应信封。page 仅 offset 模式传入,游标模式传 nil。
func NewListResponse[T any](r Result[T], total *int64, page *int, pageSize int) ListResponse[T] {
	return ListResponse[T]{
		Items:      r.Items,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		NextCursor: r.NextCursor,
		HasMore:    r.HasMore,
	}
}
