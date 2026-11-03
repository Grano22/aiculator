package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/Grano22/aiculator/internal/api"
)

func startPlugins(context.Context, *api.Server, *http.ServeMux, *slog.Logger) (func(), error) {
	return func() {}, nil
}
