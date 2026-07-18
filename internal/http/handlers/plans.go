package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/rakshithyadhav/mrp-go/internal/repo"
)

func (h *Handlers) ListPlans(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50, 500)
	offset := queryInt(r, "offset", 0, 0)

	plans, err := repo.ListPlans(r.Context(), h.pool, limit, offset)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusOK, plans)
}

type createPlanRequest struct {
	ItemCode    string  `json:"item_code"`
	Qty         float64 `json:"qty"`
	DueDate     string  `json:"due_date"` // YYYY-MM-DD
	WarehouseID int64   `json:"warehouse_id,omitempty"`
}

func (h *Handlers) CreatePlan(w http.ResponseWriter, r *http.Request) {
	var req createPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.ItemCode == "" || req.Qty <= 0 {
		respondError(w, http.StatusBadRequest, "item_code and positive qty are required")
		return
	}
	if _, err := time.Parse("2006-01-02", req.DueDate); err != nil {
		respondError(w, http.StatusBadRequest, "due_date must be YYYY-MM-DD")
		return
	}

	id, err := repo.CreatePlan(r.Context(), h.pool, repo.CreatePlanInput{
		ItemCode:    req.ItemCode,
		Qty:         req.Qty,
		DueDate:     req.DueDate,
		WarehouseID: req.WarehouseID,
	})
	if err != nil {
		respondError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	respond(w, http.StatusCreated, map[string]int64{"id": id})
}
