// Package core assembles the reusable jai engine from its parts into a single
// Engine value a frontend can build and hold. It owns no process-global state:
// each caller (CLI, GUI, long-running service) constructs its own Engine from a
// Store, a JiraClient, and a Config, so the engine is embeddable without the
// cobra CLI, the TUI, or any output concern.
package core

import (
	"github.com/sthadka/jai/v6/pkg/config"
	"github.com/sthadka/jai/v6/pkg/jira"
	"github.com/sthadka/jai/v6/pkg/metrics"
	"github.com/sthadka/jai/v6/pkg/query"
	"github.com/sthadka/jai/v6/pkg/store"
	"github.com/sthadka/jai/v6/pkg/sync"
)

// Engine is the assembled jai engine. Its fields are the interface seams
// (Store, Client) plus the sub-engines wired to them, so consumers reach for a
// built Engine instead of a package-global singleton.
type Engine struct {
	Config  *config.Config
	Store   store.Store
	Client  jira.JiraClient
	Query   *query.Engine
	Sync    *sync.Engine
	Metrics *metrics.Service
}

// New assembles an Engine from its dependencies. The Store and JiraClient are
// the injectable seams (the production implementations are *store.DB from
// store.Open and *jira.Client from jira.New/NewWithClient). The metrics service
// derives its advisory-lock sidecar from cfg.DB.Path, matching the path used to
// open the store.
func New(cfg *config.Config, st store.Store, client jira.JiraClient) *Engine {
	metricsStore := metrics.NewStore(st, cfg.DB.Path)
	pol := metrics.CompilePolicy(cfg.Metrics)
	return &Engine{
		Config:  cfg,
		Store:   st,
		Client:  client,
		Query:   query.New(st, cfg),
		Sync:    sync.New(st, client, cfg),
		Metrics: metrics.NewService(st, metricsStore, pol, cfg),
	}
}

// Close releases the engine's resources (the underlying store connection).
func (e *Engine) Close() error {
	if e.Store != nil {
		return e.Store.Close()
	}
	return nil
}
