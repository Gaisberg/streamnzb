package app

import (
	"net/http"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/next/api"
	"streamnzb/pkg/next/playback"
	"streamnzb/pkg/next/preset"
)

const DefaultListenAddr = ":7000"

type Options struct {
	Version           string
	Preset            *preset.Service
	Playback          *playback.Service
	AuthenticateToken func(string) (*auth.Device, error)
}

type App struct {
	handler http.Handler
}

func New(opts Options) *App {
	presetService := opts.Preset
	if presetService == nil {
		presetService = preset.NewService("")
	}

	playbackService := opts.Playback
	if playbackService == nil {
		playbackService = playback.NewService()
	}

	return &App{
		handler: api.NewRouter(api.Dependencies{
			Version:           opts.Version,
			Preset:            presetService,
			Playback:          playbackService,
			AuthenticateToken: opts.AuthenticateToken,
		}),
	}
}

func (a *App) Handler() http.Handler {
	return a.handler
}

func (a *App) NewServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           a.handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
}

func (a *App) ListenAndServe(addr string) error {
	return a.NewServer(addr).ListenAndServe()
}
