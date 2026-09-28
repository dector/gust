package main

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dector/gust/pkg/probe"
)

const gustDiscoveryTimeout = 5 * time.Second

type gustScanner func(context.Context) ([]probe.Instance, error)

type gustInstance struct {
	Workdir, State, TailscaleURL string
	AppPort, ProxyPort           string
	Status                       string
	Alive                        bool
}

func loadGustInstances(ctx context.Context, scan gustScanner, client *http.Client) ([]gustInstance, string) {
	if scan == nil {
		return nil, "Gust probe is unavailable."
	}
	// Include discovery and all app checks in the same request budget.
	discoveryCtx, stop := context.WithTimeout(ctx, gustDiscoveryTimeout)
	defer stop()
	found, err := scan(discoveryCtx)
	if err != nil {
		return nil, "Could not discover Gust instances: " + err.Error()
	}
	instances := make([]gustInstance, len(found))
	// Use only validated, reported URLs to choose a fallback hostname.
	hostCounts := make(map[string]int)
	for _, instance := range found {
		if safeURL := safeTailscaleURL(instance.TailscaleURL); safeURL != "" {
			hostCounts[urlHostname(safeURL)]++
		}
	}
	fallbackHost := ""
	for host, count := range hostCounts {
		if count > hostCounts[fallbackHost] || (count == hostCounts[fallbackHost] && (fallbackHost == "" || host < fallbackHost)) {
			fallbackHost = host
		}
	}
	if client == nil {
		client = localProbeClient()
	}
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, instance := range found {
		appPort, proxyPort := portString(instance.AppPort), portString(instance.ProxyPort)
		tailscaleURL := safeTailscaleURL(instance.TailscaleURL)
		if tailscaleURL == "" && fallbackHost != "" && appPort != "" {
			tailscaleURL = "https://" + fallbackHost + ":" + appPort + "/"
		}
		instances[i] = gustInstance{
			Workdir: instance.Root, State: instance.State, TailscaleURL: tailscaleURL,
			AppPort: appPort, ProxyPort: proxyPort, Status: "status unavailable",
		}
		if instance.State != "running" {
			instances[i].Status = instance.State
			continue
		}
		if appPort == "" {
			instances[i].Status = "invalid app port"
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-discoveryCtx.Done():
			continue
		}
		wg.Add(1)
		go func(i int, appPort string) {
			defer wg.Done()
			defer func() { <-sem }()
			request, err := http.NewRequestWithContext(discoveryCtx, http.MethodGet, "http://127.0.0.1:"+appPort+"/", nil)
			if err == nil {
				resp, requestErr := client.Do(request)
				if requestErr == nil {
					instances[i].Alive = true
					instances[i].Status = "responding"
					_ = resp.Body.Close()
					return
				}
			}
			instances[i].Status = "not responding"
		}(i, appPort)
	}
	wg.Wait()
	sort.SliceStable(instances, func(i, j int) bool {
		if instances[i].Alive != instances[j].Alive {
			return instances[i].Alive
		}
		return instances[i].Workdir < instances[j].Workdir
	})
	return instances, ""
}

func localProbeClient() *http.Client {
	return &http.Client{
		Timeout:       1 * time.Second,
		Transport:     &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func portString(port int) string {
	if port < 1 || port > 65535 {
		return ""
	}
	return strconv.Itoa(port)
}

func urlHostname(value string) string {
	u, _ := url.Parse(value) // value has already passed safeTailscaleURL
	return strings.ToLower(u.Hostname())
}

func safeTailscaleURL(value string) string {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || !strings.HasSuffix(strings.ToLower(u.Hostname()), ".ts.net") || u.User != nil {
		return ""
	}
	return u.String()
}
