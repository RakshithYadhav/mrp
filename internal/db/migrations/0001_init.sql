-- Core MRP schema.
-- Modeled after a production Salesforce manufacturing package (seiban-style MRP):
--   Item__c -> items, CompositionPattern__c/Composition2__c -> bom_headers/bom_lines,
--   ProcessPattern__c/Process__c -> routings/routing_steps, ProdPlan__c -> production_plans,
--   ProdOrder__c -> production_orders, WorkOrder__c -> work_orders,
--   ChildItemRequiredQuantity__c -> component_requirements,
--   PurchaseOrderRequest__c -> purchase_requests, InventoryMovement__c -> inventory_movements.
--
-- NOTE: indexes are intentionally minimal in v1. The missing indexes on
-- inventory_movements are added later as a measured optimization (see BENCHMARKS.md).

CREATE TABLE plants (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    work_start  TIME NOT NULL DEFAULT '08:00',
    work_end    TIME NOT NULL DEFAULT '17:00'
);

CREATE TABLE holidays (
    plant_id    BIGINT NOT NULL REFERENCES plants(id),
    holiday     DATE NOT NULL,
    PRIMARY KEY (plant_id, holiday)
);

CREATE TABLE warehouses (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    plant_id    BIGINT NOT NULL REFERENCES plants(id),
    code        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL
);

CREATE TABLE items (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code            TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    -- make: produced in-house via BOM+routing. buy: purchased (MRP emits purchase_requests).
    item_type       TEXT NOT NULL CHECK (item_type IN ('make', 'buy')),
    uom             TEXT NOT NULL DEFAULT 'EA',
    lead_time_days  INT  NOT NULL DEFAULT 1 CHECK (lead_time_days >= 0),
    lot_size_rule   TEXT NOT NULL DEFAULT 'lot_for_lot' CHECK (lot_size_rule IN ('lot_for_lot', 'fixed')),
    fixed_lot_size  NUMERIC CHECK (fixed_lot_size IS NULL OR fixed_lot_size > 0),
    safety_stock    NUMERIC NOT NULL DEFAULT 0 CHECK (safety_stock >= 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE bom_headers (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    item_id     BIGINT NOT NULL REFERENCES items(id),
    name        TEXT NOT NULL,
    is_active   BOOLEAN NOT NULL DEFAULT true,
    UNIQUE (item_id, name)
);

CREATE TABLE bom_lines (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    bom_header_id  BIGINT NOT NULL REFERENCES bom_headers(id),
    child_item_id  BIGINT NOT NULL REFERENCES items(id),
    qty_per        NUMERIC NOT NULL CHECK (qty_per > 0),
    -- which routing step consumes this component (mirrors Composition2__c process order no.)
    process_seq    INT NOT NULL DEFAULT 10,
    scrap_pct      NUMERIC NOT NULL DEFAULT 0 CHECK (scrap_pct >= 0 AND scrap_pct < 100)
);

CREATE TABLE resources (
    id                     BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    plant_id               BIGINT NOT NULL REFERENCES plants(id),
    code                   TEXT NOT NULL UNIQUE,
    name                   TEXT NOT NULL,
    capacity_hours_per_day NUMERIC NOT NULL DEFAULT 8
);

CREATE TABLE routings (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    item_id     BIGINT NOT NULL REFERENCES items(id),
    name        TEXT NOT NULL,
    is_active   BOOLEAN NOT NULL DEFAULT true,
    UNIQUE (item_id, name)
);

CREATE TABLE routing_steps (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    routing_id      BIGINT NOT NULL REFERENCES routings(id),
    seq             INT NOT NULL,
    name            TEXT NOT NULL,
    resource_id     BIGINT REFERENCES resources(id),
    setup_hours     NUMERIC NOT NULL DEFAULT 0,
    hours_per_unit  NUMERIC NOT NULL DEFAULT 0.01,
    UNIQUE (routing_id, seq)
);

CREATE TABLE production_plans (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code         TEXT UNIQUE,
    item_id      BIGINT NOT NULL REFERENCES items(id),
    qty          NUMERIC NOT NULL CHECK (qty > 0),
    due_date     DATE NOT NULL,
    warehouse_id BIGINT NOT NULL REFERENCES warehouses(id),
    status       TEXT NOT NULL DEFAULT 'draft'
                 CHECK (status IN ('draft', 'planned', 'released', 'done', 'error')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Async MRP run tracking (mirrors ExecutionManagement__c status polling in the original).
CREATE TABLE mrp_jobs (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    plan_id     BIGINT NOT NULL REFERENCES production_plans(id),
    status      TEXT NOT NULL DEFAULT 'queued'
                CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
    progress    INT NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
    error       TEXT,
    stats       JSONB,
    queued_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);

CREATE TABLE production_orders (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    plan_id         BIGINT NOT NULL REFERENCES production_plans(id),
    parent_order_id BIGINT REFERENCES production_orders(id),
    item_id         BIGINT NOT NULL REFERENCES items(id),
    bom_header_id   BIGINT REFERENCES bom_headers(id),
    qty             NUMERIC NOT NULL CHECK (qty > 0),
    due_date        DATE NOT NULL,
    start_date      DATE,
    status          TEXT NOT NULL DEFAULT 'planned'
                    CHECK (status IN ('planned', 'released', 'in_progress', 'done', 'cancelled')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE work_orders (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    production_order_id BIGINT NOT NULL REFERENCES production_orders(id),
    seq                 INT NOT NULL,
    name                TEXT NOT NULL,
    resource_id         BIGINT REFERENCES resources(id),
    qty                 NUMERIC NOT NULL,
    planned_start       TIMESTAMPTZ,
    planned_end         TIMESTAMPTZ,
    prev_work_order_id  BIGINT REFERENCES work_orders(id),
    status              TEXT NOT NULL DEFAULT 'planned'
                        CHECK (status IN ('planned', 'in_progress', 'done')),
    UNIQUE (production_order_id, seq)
);

-- Component demand pinned to the work order whose routing step consumes it.
CREATE TABLE component_requirements (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    work_order_id BIGINT NOT NULL REFERENCES work_orders(id),
    item_id       BIGINT NOT NULL REFERENCES items(id),
    qty_required  NUMERIC NOT NULL CHECK (qty_required > 0),
    qty_consumed  NUMERIC NOT NULL DEFAULT 0
);

CREATE TABLE purchase_requests (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    plan_id             BIGINT NOT NULL REFERENCES production_plans(id),
    production_order_id BIGINT REFERENCES production_orders(id),
    item_id             BIGINT NOT NULL REFERENCES items(id),
    qty                 NUMERIC NOT NULL CHECK (qty > 0),
    need_by             DATE NOT NULL,
    status              TEXT NOT NULL DEFAULT 'open'
                        CHECK (status IN ('open', 'ordered', 'received', 'cancelled')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Append-only inventory ledger. On-hand stock is derived (SUM of qty), never mutated in place.
-- v1 has NO secondary index here on purpose: the stock dashboard optimization is measured later.
CREATE TABLE inventory_movements (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    item_id       BIGINT NOT NULL REFERENCES items(id),
    warehouse_id  BIGINT NOT NULL REFERENCES warehouses(id),
    qty           NUMERIC NOT NULL,  -- signed: + receipt, - issue
    movement_type TEXT NOT NULL
                  CHECK (movement_type IN ('receipt', 'issue', 'adjustment', 'backflush', 'production')),
    ref_type      TEXT,
    ref_id        BIGINT,
    moved_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE work_results (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    work_order_id BIGINT NOT NULL REFERENCES work_orders(id),
    qty_good      NUMERIC NOT NULL CHECK (qty_good >= 0),
    qty_scrap     NUMERIC NOT NULL DEFAULT 0 CHECK (qty_scrap >= 0),
    reported_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
