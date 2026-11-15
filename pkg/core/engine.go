package core

import (
	"github.com/sthadka/jai/pkg/config"
	"github.com/sthadka/jai/pkg/jira"
	"github.com/sthadka/jai/pkg/metrics"
	"github.com/sthadka/jai/pkg/query"
	store "github.com/sthadka/jai/pkg/store"
	"github.com/sthadka/jai/pkg/sync"
)

// Engine is the root assembly of all reusable jai engine packages.
// It orchestrates interactions between the configured store, sync engine,
// metrics server, and query engine.
type Engine struct {
	Store   *store.DB
	Client  *jira.Client
	Config  *config.Config
	Sync    *sync.Engine
	Query   *query.Engine
	Metrics *metrics.Service
}

// New constructs a new engine from its dependencies.
func New(db *store.DB, dbPath string, client *jira.Client, cfg *config.Config) *Engine {
	metricsStore := metrics.NewStore(db, dbPath)
	return &Engine{
		Store:   db,
		Client:  client,
		Config:  cfg,
		Sync:    sync.New(db, client, cfg),
		Query:   query.New(db, cfg),
		Metrics: metrics.NewService(db, metricsStore, metrics.CompilePolicy(cfg.Metrics), cfg),
	}
}
