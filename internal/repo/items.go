package repo

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rakshithyadhav/mrp-go/internal/domain"
)

func ListItems(ctx context.Context, pool *pgxpool.Pool, search string, limit, offset int) ([]domain.Item, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, code, name, item_type, uom, lead_time_days, lot_size_rule, fixed_lot_size, safety_stock
		FROM items
		WHERE $1 = '' OR code ILIKE '%' || $1 || '%' OR name ILIKE '%' || $1 || '%'
		ORDER BY id
		LIMIT $2 OFFSET $3`, search, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []domain.Item{}
	for rows.Next() {
		var it domain.Item
		if err := rows.Scan(&it.ID, &it.Code, &it.Name, &it.ItemType, &it.UOM,
			&it.LeadTimeDays, &it.LotSizeRule, &it.FixedLotSize, &it.SafetyStock); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}
