package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/derzierau/hesper/relay/internal/auth"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/derzierau/hesper/relay/internal/relay"
	"github.com/derzierau/hesper/relay/internal/storage"
	"github.com/derzierau/hesper/relay/internal/transport"
)

func main() {
	if err := run(); err != nil {
		slog.Error("relay stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	listen := flag.String("listen", "127.0.0.1:8787", "Public API bind address; put behind HTTPS for remote access")
	data := flag.String("data", ".state", "Persistent state directory")
	admin := flag.String("admin-socket", "", "Local Unix administration socket (default DATA/admin.sock)")
	authPath := flag.String("auth-config", "", "GitHub authentication config JSON (required outside development)")
	dev := flag.Bool("dev-invitations", false, "Enable invitation authentication on loopback only")
	flag.Parse()
	if *authPath == "" && !*dev {
		return fmt.Errorf("--auth-config is required; loopback development may use --dev-invitations")
	}
	if *dev {
		host, _, err := net.SplitHostPort(*listen)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || *authPath != "" {
			return fmt.Errorf("development invitations require a loopback IP and no auth config")
		}
	}
	if *admin == "" {
		*admin = filepath.Join(*data, "admin.sock")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	repo, err := storage.Open(filepath.Join(*data, "relay.db"))
	if err != nil {
		return err
	}
	defer repo.Close()
	devices, err := repo.Devices(ctx, "")
	if err != nil {
		return err
	}
	hub := relay.New(devices, 20*time.Second)
	defer hub.Close()
	app := transport.New(repo, hub)
	defer app.Terminals.Close()
	if *authPath != "" {
		cfg, err := auth.LoadConfig(*authPath)
		if err != nil {
			return err
		}
		secret, err := auth.ReadSecret(cfg.GitHubSecretFile)
		if err != nil {
			return err
		}
		policy := auth.NewAllowlist(cfg.AllowedGitHubIDs)
		hub.SetPolicy(policy.Allowed)
		app.Policy = policy
		app.Auth = &auth.HTTP{Repository: repo, Provider: &auth.GitHub{ClientID: cfg.GitHubClientID, Secret: strings.TrimSpace(string(secret)), Callback: cfg.PublicURL + "/auth/github/callback"}, Policy: policy, PublicURL: cfg.PublicURL, AppCallbacks: cfg.AppCallbacks, Register: hub.Register, Revoke: app.Revoke}
		reload := make(chan os.Signal, 1)
		signal.Notify(reload, syscall.SIGHUP)
		defer signal.Stop(reload)
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := repo.PruneAuth(ctx); err != nil {
						slog.Error("auth cleanup failed", "error", err)
					}
				case <-reload:
					next, err := auth.LoadConfig(*authPath)
					if err != nil {
						slog.Error("allowlist reload rejected", "error", err)
						continue
					}
					previousIDs := cfg.AllowedGitHubIDs
					cfg.AllowedGitHubIDs = next.AllowedGitHubIDs
					if !reflect.DeepEqual(cfg, next) {
						cfg.AllowedGitHubIDs = previousIDs
						slog.Error("only allowedGitHubIds can change on SIGHUP; restart for other config changes")
						continue
					}
					policy.Replace(next.AllowedGitHubIDs)
					app.Terminals.CloseDenied(policy.Allowed)
					hub.SetPolicy(policy.Allowed)
					slog.Info("GitHub allowlist reloaded")
				}
			}
		}()
	}

	publicListener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer publicListener.Close()
	if err := os.MkdirAll(filepath.Dir(*admin), 0700); err != nil {
		return err
	}
	if info, err := os.Lstat(*admin); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("admin path already exists and is not a socket")
		}
		conn, err := net.DialTimeout("unix", *admin, time.Second)
		if err == nil {
			conn.Close()
			return fmt.Errorf("admin socket is already in use")
		}
		if err := os.Remove(*admin); err != nil {
			return err
		}
	}
	adminListener, err := net.Listen("unix", *admin)
	if err != nil {
		return err
	}
	defer adminListener.Close()
	if err := os.Chmod(*admin, 0600); err != nil {
		return err
	}
	public := &http.Server{Handler: app.PublicHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 8192}
	local := &http.Server{Handler: app.AdminHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: time.Minute}
	errors := make(chan error, 2)
	go func() { errors <- public.Serve(publicListener) }()
	go func() { errors <- local.Serve(adminListener) }()
	slog.Info("relay listening", "address", publicListener.Addr(), "admin", *admin)
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errors:
	}
	app.Terminals.Close()
	hub.Close()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	public.Shutdown(shutdown)
	local.Shutdown(shutdown)
	if serveErr == http.ErrServerClosed {
		return nil
	}
	return serveErr
}
