package domain

import "time"

type Item struct {
	ID           int64    `json:"id"`
	Code         string   `json:"code"`
	Name         string   `json:"name"`
	ItemType     string   `json:"item_type"` // make | buy
	UOM          string   `json:"uom"`
	LeadTimeDays int      `json:"lead_time_days"`
	LotSizeRule  string   `json:"lot_size_rule"`
	FixedLotSize *float64 `json:"fixed_lot_size,omitempty"`
	SafetyStock  float64  `json:"safety_stock"`
}

type ProductionPlan struct {
	ID          int64     `json:"id"`
	Code        string    `json:"code"`
	ItemID      int64     `json:"item_id"`
	ItemCode    string    `json:"item_code"`
	ItemName    string    `json:"item_name"`
	Qty         float64   `json:"qty"`
	DueDate     time.Time `json:"due_date"`
	WarehouseID int64     `json:"warehouse_id"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}
