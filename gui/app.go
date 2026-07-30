package main

import (
	"context"
	"errors"
	"os"
	"sync"

	sessionservice "github.com/sessionmgr/sessionmgr/internal/service"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	mu         sync.RWMutex
	ctx        context.Context
	service    *sessionservice.Service
	startupErr error
	preview    bool
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ctx = ctx
	a.preview = os.Getenv("SESSIONMGR_GUI_PREVIEW") == "1"
	if a.preview {
		return
	}
	a.service, a.startupErr = sessionservice.Open()
}

func (a *App) shutdown(context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.service != nil {
		a.startupErr = a.service.Close()
		a.service = nil
	}
}

func (a *App) GetDashboard() (sessionservice.Dashboard, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.preview {
		return sessionservice.PreviewDashboard(), nil
	}
	if a.startupErr != nil {
		return sessionservice.Dashboard{}, a.startupErr
	}
	if a.service == nil {
		return sessionservice.Dashboard{}, errors.New("Session Manager service is not ready")
	}
	return a.service.Dashboard(a.ctx)
}

func (a *App) Initialize() (sessionservice.Dashboard, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.preview {
		return sessionservice.PreviewDashboard(), nil
	}
	if a.startupErr != nil {
		return sessionservice.Dashboard{}, a.startupErr
	}
	if a.service == nil {
		return sessionservice.Dashboard{}, errors.New("Session Manager service is not ready")
	}
	return a.service.Initialize(a.ctx)
}

func (a *App) SelectWorkspace() (string, error) {
	a.mu.RLock()
	ctx := a.ctx
	a.mu.RUnlock()
	if ctx == nil {
		return "", errors.New("window is not ready")
	}
	return wailsruntime.OpenDirectoryDialog(ctx, wailsruntime.OpenDialogOptions{
		Title: "Select Git workspace",
	})
}
