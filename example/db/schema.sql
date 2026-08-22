-- example 工程 DDL + 注释 DSL(单一事实源)。
-- users 表覆盖全部 dataType 与 DSL 形态;orders 表用于跨表游标指纹互斥验证。

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

COMMENT ON COLUMN users.id         IS 'ID [@dynamic: exact, in @sortable @selectable]';
COMMENT ON COLUMN users.name       IS '姓名 [@dynamic: fuzzy, exact @sortable @selectable]';
COMMENT ON COLUMN users.email      IS '用户邮箱，如 test@example.com [@dynamic: exact, in, prefix @selectable]';
COMMENT ON COLUMN users.password   IS '密码 [@hidden]';
COMMENT ON COLUMN users.status     IS '状态 [已废弃，仅展示] [@dynamic: exact, in @sortable @selectable]';
COMMENT ON COLUMN users.role       IS '角色 [@dynamic: fuzzy @sortable]';
COMMENT ON COLUMN users.age        IS '年龄 [@dynamic: exact, in, range @sortable @selectable]';
COMMENT ON COLUMN users.score      IS '评分 [0-100] [@dynamic: range @sortable @selectable]';
COMMENT ON COLUMN users.balance    IS '余额 [元] [@dynamic: range @selectable]';
COMMENT ON COLUMN users.birth_date IS '生日 [@dynamic: eq, range @selectable]';
COMMENT ON COLUMN users.org_id     IS '组织ID [@dynamic: exact, in]';
COMMENT ON COLUMN users.tags       IS '标签 [@dynamic: in @selectable]';
COMMENT ON COLUMN users.meta       IS '元数据 [@dynamic: has]';
COMMENT ON COLUMN users.created_at IS '创建时间 [@dynamic: exact, in, range @sortable @selectable]';
COMMENT ON COLUMN users.updated_at IS '更新时间 [@dynamic: range, is_null, not_null @sortable]';
COMMENT ON COLUMN users.deleted_at IS '删除时间 [@dynamic: is_null, not_null]';
COMMENT ON TABLE  users            IS '用户表 [@default_sort: created_at DESC @pk: id @page: 20/200]';

CREATE TABLE orders (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_no     TEXT        NOT NULL,
    user_id      BIGINT      NOT NULL,
    amount_cents BIGINT      NOT NULL,
    status       order_status NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON COLUMN orders.order_no IS '订单号 [@dynamic: exact, prefix @selectable]';
COMMENT ON COLUMN orders.user_id  IS '用户ID [@dynamic: eq, in @selectable @api: /api/v1/users/options value_key=id label_key=name]';
COMMENT ON COLUMN orders.amount_cents IS '金额（分） [@dynamic: range @sortable @selectable]';
COMMENT ON COLUMN orders.status   IS '状态 [@dynamic: exact, in @sortable @selectable]';
COMMENT ON COLUMN orders.created_at IS '创建时间 [@dynamic: range @sortable @selectable]';
COMMENT ON TABLE  orders          IS '订单表 [@default_sort: created_at DESC @pk: id @page: 20/100]';
