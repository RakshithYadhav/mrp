-- Add a new column to production_orders to reference the consuming work order
ALTER TABLE production_orders
    ADD COLUMN consuming_work_order_id BIGINT REFERENCES work_orders(id);

-- Update date type to timestamp
ALTER TABLE production_orders
    ALTER COLUMN due_date   TYPE TIMESTAMPTZ USING due_date::timestamptz,
    ALTER COLUMN start_date TYPE TIMESTAMPTZ USING start_date::timestamptz;

-- Create indexes for efficient querying
CREATE INDEX IF NOT EXISTS idx_production_orders_plan ON production_orders (plan_id);
CREATE INDEX IF NOT EXISTS idx_work_orders_order      ON work_orders (production_order_id, seq);
CREATE INDEX IF NOT EXISTS idx_comp_reqs_work_order   ON component_requirements (work_order_id);