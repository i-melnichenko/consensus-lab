package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var registerRuntimeCollectorsOnce sync.Once

func (a *App) metricsServer() (*http.Server, net.Listener, error) {
	if a.config.MetricsAddr == "" {
		return nil, nil, nil
	}

	var regErr error
	registerRuntimeCollectorsOnce.Do(func() {
		if err := prometheus.DefaultRegisterer.Register(collectors.NewGoCollector()); err != nil {
			if _, ok := err.(prometheus.AlreadyRegisteredError); !ok {
				regErr = fmt.Errorf("metrics register go collector: %w", err)
				return
			}
		}
		if err := prometheus.DefaultRegisterer.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})); err != nil {
			if _, ok := err.(prometheus.AlreadyRegisteredError); !ok {
				regErr = fmt.Errorf("metrics register process collector: %w", err)
				return
			}
		}
	})
	if regErr != nil {
		return nil, nil, regErr
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	lis, err := net.Listen("tcp", a.config.MetricsAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen metrics %s: %w", a.config.MetricsAddr, err)
	}

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv, lis, nil
}

func shutdownHTTPServer(srv *http.Server, logger Logger, name string) {
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Warn(name+" shutdown failed", "error", err)
	}
}
