package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rakshithyadhav/mrp-go/internal/http/handlers"
)

func NewRouter(pool *pgxpool.Pool) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	h := handlers.New(pool)

	r.Get("/healthz", h.Health)
	r.Get("/readyz", h.Ready)

	r.Route("/api", func(r chi.Router) {
		r.Get("/items", h.ListItems)
		r.Get("/plans", h.ListPlans)
		r.Post("/plans", h.CreatePlan)
	})

	return r
}
