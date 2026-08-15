package ui

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sessionmgr/sessionmgr/internal/archive"
	"github.com/sessionmgr/sessionmgr/internal/config"
)

//go:embed static/*
var staticFiles embed.FS

type Options struct {
	Listen       string
	CodexHome    string
	DeepSeekHome string
	Repo         string
	OpenBrowser  bool
	ConfigStore  config.Store
	Ready        func(string)
	Log          io.Writer
}

type exportRequest struct {
	Directory       string `json:"directory"`
	Scope           string `json:"scope"`
	IncludeArchived bool   `json:"include_archived"`
	IncludeDeepSeek bool   `json:"include_deepseek"`
	IncludeNonGit   bool   `json:"include_non_git"`
}

type exportResponse struct {
	Result archive.Result `json:"result"`
	Error  string         `json:"error,omitempty"`
}

type sourceEnvironment struct {
	Path      string `json:"path"`
	Available bool   `json:"available"`
}

type environmentState struct {
	Platform string            `json:"platform"`
	Git      bool              `json:"git_available"`
	Codex    sourceEnvironment `json:"codex"`
	DeepSeek sourceEnvironment `json:"deepseek"`
}

func Run(ctx context.Context, opts Options) error {
	if opts.Listen == "" {
		opts.Listen = "127.0.0.1:0"
	}
	if err := validateListenAddress(opts.Listen); err != nil {
		return err
	}
	if opts.ConfigStore.Path == "" {
		store, err := config.DefaultStore()
		if err != nil {
			return err
		}
		opts.ConfigStore = store
	}
	listener, err := net.Listen("tcp", opts.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	token, err := newToken()
	if err != nil {
		return err
	}
	address := listener.Addr().String()
	if host, port, splitErr := net.SplitHostPort(address); splitErr == nil && host == "::1" {
		address = "[::1]:" + port
	}
	url := "http://" + address + "/#" + token
	handler, err := NewHandlerWithSources(token, opts.ConfigStore, opts.CodexHome, opts.DeepSeekHome, opts.Repo)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	if opts.Ready != nil {
		opts.Ready(url)
	}
	if opts.OpenBrowser {
		if err := openBrowser(url); err != nil && opts.Log != nil {
			fmt.Fprintf(opts.Log, "warning: could not open browser: %v\n", err)
		}
	}
	shutdownDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = server.Shutdown(shutdownCtx)
			cancel()
		case <-shutdownDone:
		}
	}()
	err = server.Serve(listener)
	close(shutdownDone)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func NewHandler(token string, store config.Store, codexHome, repo string) (http.Handler, error) {
	return NewHandlerWithSources(token, store, codexHome, "", repo)
}

func NewHandlerWithSources(token string, store config.Store, codexHome, deepSeekHome, repo string) (http.Handler, error) {
	if token == "" {
		return nil, fmt.Errorf("GUI API token is required")
	}
	if repo == "" {
		repo = "."
	}
	staticRoot, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", securityHeaders(http.FileServer(http.FS(staticRoot))))
	mux.HandleFunc("GET /api/state", requireToken(token, func(w http.ResponseWriter, _ *http.Request) {
		value, err := store.Load()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"schema_version": config.SchemaVersion,
			"directory":      value.ExportDirectory,
			"environment":    inspectEnvironment(codexHome, deepSeekHome),
		})
	}))
	mux.HandleFunc("PUT /api/config", requireToken(token, func(w http.ResponseWriter, request *http.Request) {
		var body struct {
			Directory string `json:"directory"`
		}
		if err := decodeJSON(w, request, &body); err != nil {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		value, err := store.SetExportDirectory(body.Directory)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"schema_version": config.SchemaVersion,
			"directory":      value.ExportDirectory,
		})
	}))
	mux.HandleFunc("POST /api/pick-directory", requireToken(token, func(w http.ResponseWriter, request *http.Request) {
		directory, err := pickDirectory(request.Context())
		if err != nil {
			writeAPIError(w, http.StatusNotImplemented, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"directory": directory})
	}))
	mux.HandleFunc("POST /api/export", requireToken(token, func(w http.ResponseWriter, request *http.Request) {
		var body exportRequest
		if err := decodeJSON(w, request, &body); err != nil {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		directory, err := store.ResolveDirectory(body.Directory, body.Directory != "")
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		device, err := store.EnsureDevice()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err)
			return
		}
		allRepos := body.Scope != "current"
		result, exportErr := archive.Export(request.Context(), archive.Options{
			CodexHome: codexHome, DeepSeekHome: deepSeekHome,
			Output: directory, Repo: repo, AllRepos: allRepos,
			IncludeArchived: body.IncludeArchived,
			IncludeDeepSeek: body.IncludeDeepSeek,
			IncludeNonGit:   body.IncludeNonGit,
			DeviceID:        device.DeviceID, DeviceName: device.DeviceName,
		})
		response := exportResponse{Result: result}
		if exportErr != nil {
			response.Error = exportErr.Error()
		}
		writeJSON(w, http.StatusOK, response)
	}))
	return mux, nil
}

func inspectEnvironment(codexHome, deepSeekHome string) environmentState {
	if strings.TrimSpace(codexHome) == "" {
		codexHome, _ = archive.DefaultCodexHome()
	}
	if strings.TrimSpace(deepSeekHome) == "" {
		deepSeekHome, _ = archive.DefaultDeepSeekHome()
	}
	_, gitErr := exec.LookPath("git")
	return environmentState{
		Platform: runtime.GOOS,
		Git:      gitErr == nil,
		Codex:    inspectSourceEnvironment(codexHome),
		DeepSeek: inspectSourceEnvironment(deepSeekHome),
	}
}

func inspectSourceEnvironment(home string) sourceEnvironment {
	home = strings.TrimSpace(home)
	if home == "" {
		return sourceEnvironment{}
	}
	if absolute, err := filepath.Abs(home); err == nil {
		home = absolute
	}
	info, err := os.Stat(filepath.Join(home, "sessions"))
	return sourceEnvironment{Path: home, Available: err == nil && info.IsDir()}
}

func requireToken(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Sessionmgr-Token") != token {
			writeAPIError(w, http.StatusUnauthorized, fmt.Errorf("invalid GUI API token"))
			return
		}
		next(w, request)
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, request)
	})
}

func decodeJSON(w http.ResponseWriter, request *http.Request, target interface{}) error {
	request.Body = http.MaxBytesReader(w, request.Body, 1024*1024)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("request body must contain one JSON value")
		}
		return fmt.Errorf("invalid trailing JSON data: %w", err)
	}
	return nil
}

func writeAPIError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func newToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func validateListenAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", address, err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("GUI may listen only on loopback, got %q", host)
	}
	return nil
}

func openBrowser(url string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	return command.Start()
}
