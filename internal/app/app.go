package app

import (
	"proxyctl/internal/config"
	"proxyctl/internal/renderer"
	"proxyctl/internal/runtime"
	"proxyctl/internal/storage"
	"proxyctl/internal/subscription"
)

// App wires the core module boundaries for the CLI layer.
type App struct {
	Config       config.AppConfig
	Store        storage.Store
	Runtime      runtime.Manager
	Renderer     renderer.Service
	Subscription subscription.Service
}

// New creates an application container with the provided dependencies.
func New(cfg config.AppConfig, store storage.Store, rt runtime.Manager, rnd renderer.Service, sub subscription.Service) *App {
	return &App{
		Config:       cfg,
		Store:        store,
		Runtime:      rt,
		Renderer:     rnd,
		Subscription: sub,
	}
}
