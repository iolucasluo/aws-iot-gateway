package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/explorexsignal"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	authorizationToken := strings.TrimSpace(os.Getenv("EXPLOREX_SIGNAL_AUTH_TOKEN"))
	publicBaseURL := strings.TrimSpace(os.Getenv("EXPLOREX_SIGNAL_PUBLIC_BASE_URL"))
	listenAddress := strings.TrimSpace(os.Getenv("EXPLOREX_SIGNAL_LISTEN_ADDRESS"))
	if listenAddress == "" {
		listenAddress = "127.0.0.1:18084"
	}
	if authorizationToken == "" || publicBaseURL == "" {
		return fmt.Errorf("EXPLOREX_SIGNAL_AUTH_TOKEN and EXPLOREX_SIGNAL_PUBLIC_BASE_URL are required")
	}
	iceServers, err := loadICEServers(os.Getenv("EXPLOREX_SIGNAL_ICE_SERVERS_JSON"))
	if err != nil {
		return err
	}
	service, err := explorexsignal.NewServer(explorexsignal.ServerConfig{
		AuthorizationToken: authorizationToken,
		PublicBaseURL:      publicBaseURL,
		ICEServers:         iceServers,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go service.Run(ctx)
	httpServer := &http.Server{
		Addr:              listenAddress,
		Handler:           service.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	err = httpServer.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func loadICEServers(raw string) ([]provider.ICEServer, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var servers []provider.ICEServer
	if err := json.Unmarshal([]byte(raw), &servers); err != nil {
		return nil, fmt.Errorf("parse EXPLOREX_SIGNAL_ICE_SERVERS_JSON: %w", err)
	}
	return servers, nil
}
