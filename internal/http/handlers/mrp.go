package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rakshithyadhav/mrp-go/internal/mrp"
)

// RunMRP explodes one plan's BOM into orders and purchase requests (FR-3/FR-4).
// Synchronous for now — the request blocks on the whole run; Day 3 moves this to
// an async worker with SSE progress (FR-6).
func (h *Handlers) RunMRP(w http.ResponseWriter, r *http.Request) {
	planID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || planID <= 0 {
		respondError(w, http.StatusBadRequest, "invalid plan id")
		return
	}

	result, err := mrp.New(h.pool).Explode(r.Context(), planID)
	switch {
	case errors.Is(err, mrp.ErrPlanNotFound):
		respondError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, mrp.ErrAlreadyExploded):
		respondError(w, http.StatusConflict, err.Error())
	case errors.Is(err, mrp.ErrCycle), errors.Is(err, mrp.ErrNotMakeItem):
		respondError(w, http.StatusUnprocessableEntity, err.Error())
	case err != nil:
		respondError(w, http.StatusInternalServerError, err.Error())
	default:
		respond(w, http.StatusOK, result)
	}
}
