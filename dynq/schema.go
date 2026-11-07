package dynq

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// FieldAPI 是 options.ui 的 api 选项数据源契约(字段级,前端消费)。
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
// (data_type, ops, sortable, visibility) 派生,不再生成第二份副本。
type FieldSchema struct {
	Field    string `json:"field"`
	Label    string `json:"label"`
	DataType string `json:"dataType"`
	Nullable bool   `json:"nullable"`
	Sortable bool   `json:"sortable"`
	// Visibility 是前端列表初始显隐:always(固定显示)/ optional(默认显示、
	// 可切换)/ hidden(默认收起)。零值按 optional 输出,JSON 键始终存在。
	// 与 @internal 不同:@internal 字段不进入 schema,visibility 只决定
	// 已进入前端的列默认怎么显示,不参与服务端校验。
	Visibility string    `json:"visibility"`
	Ops        []string  `json:"ops"`
	Enum       []string  `json:"enum,omitempty"`
	API        *FieldAPI `json:"api,omitempty"` // options.ui:选项数据源为远程接口
	// Desc 是字段描述(options.ui);omitempty 保证未声明的存量资源 schema
	// 字节级不变(ETag hash 不漂移)。
	Desc string `json:"desc,omitempty"`
	// Options 是静态字典选项(options.ui);omitempty 语义同 Desc。
	Options []FieldOption `json:"options,omitempty"`
}

// inferAPIMode 推导 options.ui api 的交互模式:显式声明优先;否则
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
// fields 是展示字段集合(非 @internal 字段扣除 opts.SelectDisabled),
// 不等于协议能力全集:
//   - @internal 字段是协议面永不可见,不进入 schema;
//   - 被 opts.SelectDisabled 移除的字段不进入 schema;
//   - 其余字段全部出现,即使 op 集为空或不可排序(纯投影列同样可展示);
//   - visibility 只决定前端初始显隐,不影响默认投影。
type ResourceSchema struct {
	Resource    string           `json:"resource"`
	Fields      []FieldSchema    `json:"fields"`
	DefaultSort []OrderBy        `json:"defaultSort"`
	Pagination  PaginationSchema `json:"pagination"`
}

// BuildSchema 由 registry 与 QueryConfig 产出前端 schema。
// 与 registry 由同一模板循环产出,再经 opts 做与 Parse 完全相同的减法投影:
// where 禁用 → 该字段 op 集清空;sort 禁用 → sortable = false;select 禁用 →
// 字段从 fields 移除。@internal 字段与被 opts.SelectDisabled 移除的字段不出现。
// options.ui 的 order 已标注字段按序号升序排到前面,未标注字段保持遍历序
// (= DDL 声明序)殿后,同号按声明序稳定排序。
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
	// schemaField 是字段与其 options.ui order 的排序载体:fields 与 order 必须同步
	// 交换,平行双切片 + 按位置索引比较会在交换后错位。
	type schemaField struct {
		fs    FieldSchema
		order *int
	}
	fields := []schemaField{}
	for _, f := range meta.Fields {
		if f.Hidden {
			continue
		}
		if cfg != nil && cfg.SelectDisabled(f.Key) {
			continue
		}
		visibility := f.Visibility
		if visibility == "" {
			visibility = VisibilityOptional
		}
		fs := FieldSchema{
			Field:      string(f.Key),
			Label:      f.Label,
			DataType:   string(f.DataType),
			Nullable:   f.Nullable,
			Sortable:   f.Sortable,
			Visibility: string(visibility),
			Enum:       f.Enum,
			Desc:       f.Desc,
			Options:    f.Options,
		}
		if cfg == nil || !cfg.WhereDisabled(f.Key) {
			for _, op := range f.Ops {
				fs.Ops = append(fs.Ops, string(op))
			}
		}
		if cfg != nil && cfg.SortDisabled(f.Key) {
			fs.Sortable = false
		}
		if f.API != nil {
			api := *f.API
			api.Mode = inferAPIMode(f)
			fs.API = &api
		}
		if fs.Ops == nil {
			fs.Ops = []string{}
		}
		fields = append(fields, schemaField{fs: fs, order: f.Order})
	}
	// order 投影:已标注升序在前,未标注殿后;SliceStable 保证同号与未标注
	// 段维持声明序(确定性)。
	sort.SliceStable(fields, func(i, j int) bool {
		oi, oj := fields[i].order, fields[j].order
		switch {
		case oi == nil || oj == nil:
			return oi != nil && oj == nil
		default:
			return *oi < *oj
		}
	})
	s.Fields = make([]FieldSchema, len(fields))
	for i, sf := range fields {
		s.Fields[i] = sf.fs
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
