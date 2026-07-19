package handlers

import (
	"net/http"
	"github.com/rakshithyadhav/mrp-go/internal/repo"
)

func (h *Handlers) ListItems (w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50, 500)
	offset := queryInt(r, "offset", 0, 0)
	search := r.URL.Query().Get("q")

	items, err := repo.ListItems(r.Context(), h.pool, search, limit, offset)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusOK, items)
}