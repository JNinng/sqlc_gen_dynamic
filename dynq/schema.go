package dynq

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// FieldAPI 是 @api 的选项数据源契约(字段级,前端消费)。
// 该字段 where 值控件的选项不来自静态 enum,而是 GET/POST {endpoint} 拉取:
// 响应 body 为 items 数组,每项含 valueKey / labelKey 命名的顶层字段
// (可复用本插件的 ListXxx 查询方法,或独立 options 接口)。
// JSON 协议面统一小驼峰(接口交互契约)。
type FieldAPI struct {
	Endpoint string `json:"endpoint"` // 相对前端 baseURL 的路径;查询参数由前端按协议拼
	ValueKey string `json:"valueKey"` // items 中选项值的字段名(顶层)
	LabelKey string `json:"labelKey"` // items 中展示文本的字段名(顶层)
	Mode     string `json:"mode"`     // single | multi | search(显式声明或按 ops 推导的最终值)
}

// FieldSchema 是 schema 中的单个字段契约。
// 不携带 ui 字段:控件族由 data_type 唯一确定(映射表见 schema.md/desc §10.2),
// 操作符渲染的唯一事实源是同层 ops——所有渲染信息均可由
// (data_type, ops, sortable, selectable) 派生,不再生成第二份副本。
type FieldSchema struct {
	Field      string    `json:"field"`
	Label      string    `json:"label"`
	DataType   string    `json:"dataType"`
	Nullable   bool      `json:"nullable"`
	Sortable   bool      `json:"sortable"`
	Selectable bool      `json:"selectable"`
	Ops        []string  `json:"ops"`
	Enum       []string  `json:"enum,omitempty"`
	API        *FieldAPI `json:"api,omitempty"` // @api 声明:选项数据源为远程接口
}

// inferAPIMode 推导 @api 的交互模式:显式声明优先;否则
// ops 含 in → multi(多选);含 fuzzy 三 op → search(远程搜索);否则 single。
func inferAPIMode(f FieldMeta) string {
	if f.API != nil && f.API.Mode != "" {
		return f.API.Mode
	}
	if f.HasOp(OpIn) {
		return "multi"
	}
	if f.HasOp(OpContains) || f.HasOp(OpPrefix) || f.HasOp(OpSuffix) {
		return "search"
	}
	return "single"
}

// PaginationSchema 描述分页双模式与 includeTotal。
type PaginationSchema struct {
	DefaultPageSize int      `json:"defaultPageSize"`
	MaxPageSize     int      `json:"maxPageSize"`
	Modes           []string `json:"modes"`        // ["offset", "cursor"]
	IncludeTotal    bool     `json:"includeTotal"` // 支持 includeTotal=false 跳过 COUNT
}

// ResourceSchema 是前端契约出口:资源名、字段列表、默认排序、分页参数。
// 前端拿到的即有效能力全集:被禁用字段根本不出现在 schema 中,
// QueryBuilder 无需感知"禁用"概念。
type ResourceSchema struct {
	Resource    string           `json:"resource"`
	Fields      []FieldSchema    `json:"fields"`
	DefaultSort []OrderBy        `json:"defaultSort"`
	Pagination  PaginationSchema `json:"pagination"`
}

// BuildSchema 由 registry 与 QueryConfig 产出前端 schema。
// 与 registry 由同一模板循环产出,再经 opts 做与 Parse 完全相同的减法投影:
// where 禁用 → 该字段 op 集清空;sort 禁用 → sortable = false;select 禁用 →
// selectable = false;字段被完全禁用(或原 @hidden)则不出现在 schema 中。
// 同一 opts 切片产出字节级相同的 schema(确定性纯函数)。
func BuildSchema(meta TableMeta, cfg *QueryConfig) ResourceSchema {
	s := ResourceSchema{
		Resource: meta.Resource,
		Pagination: PaginationSchema{
			DefaultPageSize: meta.PageDefault,
			MaxPageSize:     meta.PageMax,
			Modes:           []string{"offset", "cursor"},
			IncludeTotal:    true,
		},
	}
	for _, d := range meta.DefaultSort {
		s.DefaultSort = append(s.DefaultSort, d)
	}
	if s.DefaultSort == nil {
		s.DefaultSort = []OrderBy{{Field: meta.Tiebreaker}}
	}
	for _, f := range meta.Fields {
		if f.Hidden {
			continue
		}
		fs := FieldSchema{
			Field:      string(f.Key),
			Label:      f.Label,
			DataType:   string(f.DataType),
			Nullable:   f.Nullable,
			Sortable:   f.Sortable,
			Selectable: f.Selectable,
			Enum:       f.Enum,
		}
		if cfg == nil || !cfg.WhereDisabled(f.Key) {
			for _, op := range f.Ops {
				fs.Ops = append(fs.Ops, string(op))
			}
		}
		if cfg != nil && cfg.SortDisabled(f.Key) {
			fs.Sortable = false
		}
		if cfg != nil && cfg.SelectDisabled(f.Key) {
			fs.Selectable = false
		}
		if f.API != nil {
			api := *f.API
			api.Mode = inferAPIMode(f)
			fs.API = &api
		}
		if len(fs.Ops) == 0 && !fs.Sortable && !fs.Selectable {
			continue // 完全禁用 → 不出现在 schema
		}
		if fs.Ops == nil {
			fs.Ops = []string{}
		}
		s.Fields = append(s.Fields, fs)
	}
	if s.Fields == nil {
		s.Fields = []FieldSchema{}
	}
	return s
}

// Hash 返回 schema 内容 hash(ETag 用):确定性纯函数,不同禁用视图得到不同 hash。
func (s ResourceSchema) Hash() string {
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}
