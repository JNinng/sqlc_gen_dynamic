-- example 工程 DDL + 注释 DSL(单一事实源)。
-- users 表覆盖全部 dataType 与 DSL 形态;orders 表用于跨表游标指纹互斥验证;
-- customer 表演示 @scope 行级数据权限:守卫函数定义在本文件内(sqlc 可见),
-- 随迁移版本化,权限规则只在函数里写一次。

CREATE TYPE user_status AS ENUM ('active', 'inactive', 'banned');
CREATE TYPE order_status AS ENUM ('pending', 'paid', 'shipped', 'closed');

CREATE TABLE users (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name        TEXT        NOT NULL,
    email       TEXT        NOT NULL UNIQUE,
    password    TEXT        NOT NULL,
    status      user_status NOT NULL DEFAULT 'active',
    role        TEXT,
    age         INT,
    score       DOUBLE PRECISION,
    balance     NUMERIC(12, 2),
    birth_date  DATE,
    org_id      UUID,
    tags        INT[],
    meta        JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ,
    deleted_at  TIMESTAMPTZ
);

COMMENT ON COLUMN users.id         IS 'ID [@dynamic: exact, in @sortable @hideable]';
COMMENT ON COLUMN users.name       IS '姓名 [@dynamic: fuzzy, exact @sortable @hideable]';
COMMENT ON COLUMN users.email      IS '用户邮箱，如 test@example.com [@dynamic: exact, in, prefix @hideable]';
COMMENT ON COLUMN users.password   IS '密码 [@internal]';
COMMENT ON COLUMN users.status     IS '状态 [已废弃，仅展示] [@dynamic: exact, in @sortable @hideable]';
COMMENT ON COLUMN users.role       IS '角色 [@dynamic: fuzzy @sortable]';
COMMENT ON COLUMN users.age        IS '年龄 [@dynamic: exact, in, range @sortable @hideable]';
COMMENT ON COLUMN users.score      IS '评分 [0-100] [@dynamic: range @sortable @hideable]';
COMMENT ON COLUMN users.balance    IS '余额 [元] [@dynamic: range @hideable]';
COMMENT ON COLUMN users.birth_date IS '生日 [@dynamic: eq, range @hideable]';
COMMENT ON COLUMN users.org_id     IS '组织ID [@dynamic: exact, in]';
COMMENT ON COLUMN users.tags       IS '标签 [@dynamic: in @hideable]';
COMMENT ON COLUMN users.meta       IS '元数据 [@dynamic: has]';
COMMENT ON COLUMN users.created_at IS '创建时间 [@dynamic: exact, in, range @sortable @hideable]';
COMMENT ON COLUMN users.updated_at IS '更新时间 [@dynamic: range, is_null, not_null @sortable]';
COMMENT ON COLUMN users.deleted_at IS '删除时间 [@dynamic: is_null, not_null]';
COMMENT ON TABLE  users            IS '用户表 [@default_sort: created_at DESC @pk: id @page: 20/200 @filter: deleted_at is_null]';

CREATE TABLE orders (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_no     TEXT        NOT NULL,
    user_id      BIGINT      NOT NULL,
    amount_cents BIGINT      NOT NULL,
    status       order_status NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON COLUMN orders.order_no IS '订单号 [@dynamic: exact, prefix @hideable]';
COMMENT ON COLUMN orders.user_id  IS '用户ID [@dynamic: eq, in @hideable @api: /api/v1/users/options value_key=id label_key=name]';
COMMENT ON COLUMN orders.amount_cents IS '金额（分） [@dynamic: range @sortable @hideable]';
COMMENT ON COLUMN orders.status   IS '状态 [@dynamic: exact, in @sortable @hideable]';
COMMENT ON COLUMN orders.created_at IS '创建时间 [@dynamic: range @sortable @hideable]';
COMMENT ON TABLE  orders          IS '订单表 [@default_sort: created_at DESC @pk: id @page: 20/100 @filter: amount_cents gte 0]';

-- ---- 行级数据权限(@scope 演示)----
-- 部门闭包表:ancestor → descendant,含自身配对(d_eng → d_eng);
-- scope=3(本部门及子部门)经闭包展开,scope=2(本部门)精确匹配本部门。
CREATE TABLE sys_dept_closure (
    ancestor_id   varchar(64) NOT NULL,
    descendant_id varchar(64) NOT NULL,
    PRIMARY KEY (ancestor_id, descendant_id)
);

-- 员工表:权限主体。owner_dept_id 是其所属部门,tenant_id 是租户归属。
-- 启用 in_any 数组三态守卫:fn_perm_user_ids 返回 NULL(全租户)→ 放行全部、
-- {}(无任何授权)→ 拒绝全部、非空 → id = ANY(...) 精确集合。
CREATE TABLE sys_user (
    id            varchar(64) PRIMARY KEY,
    name          varchar(64) NOT NULL,
    owner_dept_id varchar(64) NOT NULL,
    tenant_id     varchar(64) NOT NULL,
    deleted_at    TIMESTAMPTZ
);

COMMENT ON COLUMN sys_user.id   IS 'ID [@dynamic: exact]';
COMMENT ON COLUMN sys_user.name IS '姓名 [@dynamic: fuzzy]';
COMMENT ON TABLE sys_user IS '员工表 [@scope: id in_any fn_perm_user_ids(user_id:varchar, scopes:int[]) @pk: id]';

-- 显式共享授权:某资源的单条记录被显式共享给某用户(守卫函数消费)。
CREATE TABLE sys_resource_grant (
    resource  varchar(64) NOT NULL,
    user_id   varchar(64) NOT NULL,
    record_id bigint      NOT NULL,
    PRIMARY KEY (resource, user_id, record_id)
);

-- fn_accessible_user_ids 返回 p_user_id 在 p_scopes(多角色并集,int[])下
-- 可见的用户 ID 集合。显式参数(不读会话变量),STABLE 标记允许优化器复用;
-- scope 语义:1=仅本人 2=本部门(精确) 3=本部门及子部门(闭包) 7=全租户,
-- 多角色 = 任一角色授予即可见(ANY 语义)。
CREATE OR REPLACE FUNCTION fn_accessible_user_ids(
    p_user_id varchar(64),
    p_scopes  int[]
)
RETURNS TABLE (user_id varchar(64)) AS $$
DECLARE
    v_dept_id varchar(64);
BEGIN
    SELECT owner_dept_id INTO v_dept_id FROM sys_user WHERE id = p_user_id;
    RETURN QUERY
        SELECT u.id FROM sys_user u
        WHERE u.deleted_at IS NULL AND u.id = p_user_id AND 1 = ANY(p_scopes)
        UNION ALL
        SELECT u.id FROM sys_user u
        WHERE u.deleted_at IS NULL AND u.owner_dept_id = v_dept_id AND 2 = ANY(p_scopes)
        UNION ALL
        SELECT u.id FROM sys_user u
        WHERE u.deleted_at IS NULL AND 3 = ANY(p_scopes)
          AND u.owner_dept_id IN (
              SELECT descendant_id FROM sys_dept_closure WHERE ancestor_id = v_dept_id
          )
        UNION ALL
        SELECT u.id FROM sys_user u
        WHERE u.deleted_at IS NULL AND 7 = ANY(p_scopes);
END;
$$ LANGUAGE plpgsql STABLE;

-- fn_accessible_dept_ids 返回 p_user_id 在 p_scopes 下可见的部门 ID 集合。
-- scope 语义:2=本部门(精确) 3=本部门及子部门(闭包);1/7 返回空集
-- (仅本人与全租户经用户分支表达,部门分支不再放行)。
CREATE OR REPLACE FUNCTION fn_accessible_dept_ids(
    p_user_id varchar(64),
    p_scopes  int[]
)
RETURNS TABLE (dept_id varchar(64)) AS $$
DECLARE
    v_dept_id varchar(64);
BEGIN
    SELECT owner_dept_id INTO v_dept_id FROM sys_user WHERE id = p_user_id;
    RETURN QUERY
        SELECT d.descendant_id FROM sys_dept_closure d
        WHERE d.ancestor_id = v_dept_id AND d.descendant_id = v_dept_id AND 2 = ANY(p_scopes)
        UNION ALL
        SELECT d.descendant_id FROM sys_dept_closure d
        WHERE d.ancestor_id = v_dept_id AND 3 = ANY(p_scopes);
END;
$$ LANGUAGE plpgsql STABLE;

-- fn_permitted 返回资源 p_resource 中被显式共享给 p_user_id 的记录 ID 集合。
-- 资源名经 @scope 字面量参数内联('customers'),user_id 走占位符。
CREATE OR REPLACE FUNCTION fn_permitted(
    p_resource varchar(64),
    p_user_id  varchar(64)
)
RETURNS TABLE (id bigint) AS $$
    SELECT g.record_id FROM sys_resource_grant g
    WHERE g.resource = p_resource AND g.user_id = p_user_id;
$$ LANGUAGE sql STABLE;

-- fn_tenant_id 返回 p_user_id 所属租户(标量守卫:eq 比较,单次求值)。
CREATE OR REPLACE FUNCTION fn_tenant_id(p_user_id varchar(64))
RETURNS varchar AS $$
    SELECT tenant_id FROM sys_user WHERE id = p_user_id;
$$ LANGUAGE sql STABLE;

-- fn_perm_user_ids 返回 p_user_id 在 p_scopes 下可见的用户 ID 数组(in_any
-- 三态契约):7=ANY(p_scopes) → NULL(全租户,显式放行);无任何授权命中 →
-- '{}'(拒绝全部);否则 → 可见 ID 数组(id = ANY(...) 精确匹配)。与集合版
-- fn_accessible_user_ids 同源语义,以数组承载以区分 NULL 与空集。
CREATE OR REPLACE FUNCTION fn_perm_user_ids(
    p_user_id varchar(64),
    p_scopes  int[]
)
RETURNS varchar[] AS $$
DECLARE
    v_dept_id varchar(64);
    v_ids     varchar[];
BEGIN
    IF 7 = ANY(p_scopes) THEN
        RETURN NULL;
    END IF;
    SELECT owner_dept_id INTO v_dept_id FROM sys_user WHERE id = p_user_id;
    SELECT array_agg(u.id) INTO v_ids
    FROM sys_user u
    WHERE u.deleted_at IS NULL AND (
        (u.id = p_user_id AND 1 = ANY(p_scopes))
        OR (u.owner_dept_id = v_dept_id AND 2 = ANY(p_scopes))
        OR (3 = ANY(p_scopes) AND u.owner_dept_id IN (
            SELECT descendant_id FROM sys_dept_closure WHERE ancestor_id = v_dept_id
        ))
    );
    RETURN COALESCE(v_ids, '{}');
END;
$$ LANGUAGE plpgsql STABLE;

-- customers 表:三条守卫 OR 组合——归属用户集合 / 归属部门集合 / 显式共享;
-- 守卫列 @internal:归属列只参与权限注入,不暴露给前端过滤/投影。
CREATE TABLE customers (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name          TEXT        NOT NULL,
    amount_cents  BIGINT      NOT NULL DEFAULT 0,
    owner_user_id varchar(64) NOT NULL,
    owner_dept_id varchar(64) NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ
);

COMMENT ON COLUMN customers.id   IS 'ID [@dynamic: exact @sortable @hideable]';
COMMENT ON COLUMN customers.name IS '客户名 [@dynamic: fuzzy @sortable @hideable]';
COMMENT ON COLUMN customers.amount_cents IS '金额（分） [@dynamic: range @sortable @hideable]';
COMMENT ON COLUMN customers.owner_user_id IS '归属用户ID [@internal]';
COMMENT ON COLUMN customers.owner_dept_id IS '归属部门ID [@internal]';
COMMENT ON COLUMN customers.created_at IS '创建时间 [@dynamic: range @sortable @hideable]';
COMMENT ON COLUMN customers.deleted_at  IS '删除时间 [@dynamic: is_null, not_null]';
COMMENT ON TABLE  customers IS '客户表 [@scope: owner_user_id in fn_accessible_user_ids(user_id:varchar, scopes:int[]) owner_dept_id in fn_accessible_dept_ids(user_id:varchar, scopes:int[]) id in fn_permitted(''customers'', user_id:varchar) @default_sort: created_at DESC @pk: id @page: 20/100]';

-- projects 表:eq 标量守卫——租户隔离:tenant_id = fn_tenant_id(user_id)。
CREATE TABLE projects (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name          TEXT        NOT NULL,
    tenant_id     varchar(64) NOT NULL,
    owner_user_id varchar(64) NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON COLUMN projects.id   IS 'ID [@dynamic: exact @sortable @hideable]';
COMMENT ON COLUMN projects.name IS '项目名 [@dynamic: fuzzy @sortable @hideable]';
COMMENT ON COLUMN projects.tenant_id IS '租户ID [@internal]';
COMMENT ON COLUMN projects.owner_user_id IS '归属用户ID [@internal]';
COMMENT ON COLUMN projects.created_at IS '创建时间 [@dynamic: range @sortable @hideable]';
COMMENT ON TABLE  projects IS '项目表 [@scope: tenant_id eq fn_tenant_id(user_id:varchar) @default_sort: created_at DESC @pk: id @page: 20/100 @filter: name eq ''template'']';

-- audit_logs 表:pk-only 最小接入形态——表级 @pk 单独入选,零 @dynamic 列:
-- 仅生成 List/Count/Parse/Schema 骨架(无过滤能力),验证零 ops 产物可编译。
CREATE TABLE audit_logs (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_id   BIGINT      NOT NULL,
    action     TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON COLUMN audit_logs.id         IS 'ID';
COMMENT ON COLUMN audit_logs.actor_id   IS '操作人';
COMMENT ON COLUMN audit_logs.action     IS '动作';
COMMENT ON COLUMN audit_logs.created_at IS '时间';
COMMENT ON TABLE  audit_logs IS '审计日志 [@pk: id]';
