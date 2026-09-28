package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/dector/gust/pkg/probe"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dector/gust/hub/views"
)

func scanner(instances ...probe.Instance) gustScanner {
	return func(ctx context.Context) ([]probe.Instance, error) {
		if _, ok := ctx.Deadline(); !ok {
			return nil, errors.New("missing discovery timeout")
		}
		return instances, nil
	}
}

func TestLoadGustInstancesSortsAndRetainsUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	port, err := strconv.Atoi(strings.TrimPrefix(server.URL, "http://127.0.0.1:"))
	if err != nil {
		t.Fatal(err)
	}
	instances, discoveryError := loadGustInstances(context.Background(), scanner(
		probe.Instance{Root: "/z-running", State: "running", AppPort: port, ProxyPort: 8000, TailscaleURL: "https://safe.example"},
		probe.Instance{Root: "/a-running", State: "running", AppPort: port, ProxyPort: 8001, TailscaleURL: "https://safe.example.ts.net"},
		probe.Instance{Root: "/broken", State: "running", AppPort: 0},
	), localProbeClient())
	if discoveryError != "" {
		t.Fatal(discoveryError)
	}
	if len(instances) != 3 {
		t.Fatalf("want 3 entries, got %d", len(instances))
	}
	if instances[0].Workdir != "/a-running" || instances[1].Workdir != "/z-running" || instances[2].Workdir != "/broken" {
		t.Fatalf("unexpected order: %#v", instances)
	}
	if !instances[0].Alive || instances[0].Status != "responding" || instances[0].AppPort != strconv.Itoa(port) || instances[0].ProxyPort != "8001" || instances[0].TailscaleURL != "https://safe.example.ts.net" {
		t.Fatalf("unexpected alive instance: %#v", instances[0])
	}
	if instances[1].TailscaleURL != "https://safe.example.ts.net:"+strconv.Itoa(port)+"/" || instances[2].Alive || instances[2].Status != "invalid app port" {
		t.Fatalf("unexpected unavailable entries: %#v", instances)
	}
}

func TestLoadGustInstancesRetainsMultipleServersPerWorkdir(t *testing.T) {
	instances, err := loadGustInstances(context.Background(), scanner(
		probe.Instance{SocketPath: "/tmp/gust-a.sock", Root: "/same/workdir", State: "stopped", AppPort: 4100, TailscaleURL: "https://host.ts.net:4100/"},
		probe.Instance{SocketPath: "/tmp/gust-b.sock", Root: "/same/workdir", State: "stopped", AppPort: 4200, TailscaleURL: "https://host.ts.net:4200/"},
	), nil)
	if err != "" {
		t.Fatal(err)
	}
	if len(instances) != 2 || instances[0].Workdir != "/same/workdir" || instances[1].Workdir != "/same/workdir" || instances[0].AppPort != "4100" || instances[1].AppPort != "4200" || instances[0].TailscaleURL != "https://host.ts.net:4100/" || instances[1].TailscaleURL != "https://host.ts.net:4200/" {
		t.Fatalf("expected two distinct servers in one workdir, got %#v", instances)
	}
}

func TestFallbackTailscaleURL(t *testing.T) {
	instances, err := loadGustInstances(context.Background(), scanner(
		probe.Instance{Root: "/reported", State: "stopped", AppPort: 4000, TailscaleURL: "https://factory.example.ts.net:4000/some/path"},
		probe.Instance{Root: "/reported-too", State: "stopped", AppPort: 4001, TailscaleURL: "https://factory.example.ts.net:4001/"},
		probe.Instance{Root: "/other", State: "stopped", AppPort: 4002, TailscaleURL: "https://other.example.ts.net:4002/"},
		probe.Instance{Root: "/wit/v2", State: "stopped", AppPort: 5200},
		probe.Instance{Root: "/bad-url", State: "stopped", AppPort: 5201, TailscaleURL: "https://attacker.example/"},
		probe.Instance{Root: "/bad-port", State: "stopped", AppPort: 70000},
	), nil)
	if err != "" {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for _, instance := range instances {
		got[instance.Workdir] = instance.TailscaleURL
	}
	for path, want := range map[string]string{
		"/reported":     "https://factory.example.ts.net:4000/some/path",
		"/reported-too": "https://factory.example.ts.net:4001/",
		"/other":        "https://other.example.ts.net:4002/",
		"/wit/v2":       "https://factory.example.ts.net:5200/",
		"/bad-url":      "https://factory.example.ts.net:5201/",
		"/bad-port":     "",
	} {
		if got[path] != want {
			t.Errorf("%s URL = %q, want %q", path, got[path], want)
		}
	}
}

func TestFallbackHostnameTieAndMissingHost(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sources []probe.Instance
		want    string
	}{
		{"tie", []probe.Instance{
			{Root: "/z", State: "stopped", TailscaleURL: "https://z.example.ts.net:1234/"},
			{Root: "/a", State: "stopped", TailscaleURL: "https://a.example.ts.net:1234/"},
			{Root: "/target", State: "stopped", AppPort: 5200},
		}, "https://a.example.ts.net:5200/"},
		{"missing", []probe.Instance{
			{Root: "/unsafe", State: "stopped", TailscaleURL: "https://example.com/"},
			{Root: "/target", State: "stopped", AppPort: 5200},
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instances, err := loadGustInstances(context.Background(), scanner(tc.sources...), nil)
			if err != "" {
				t.Fatal(err)
			}
			for _, instance := range instances {
				if instance.Workdir == "/target" && instance.TailscaleURL != tc.want {
					t.Errorf("fallback URL = %q, want %q", instance.TailscaleURL, tc.want)
				}
			}
		})
	}
}

func TestOnlyRunningInstancesAreProbed(t *testing.T) {
	instances, _ := loadGustInstances(context.Background(), scanner(
		probe.Instance{Root: "/stopped", State: "stopped", AppPort: 5200, ProxyPort: 5201},
		probe.Instance{Root: "/evil", State: "running", AppPort: 70000, ProxyPort: 10},
	), localProbeClient())
	if len(instances) != 2 || instances[0].Workdir != "/evil" || instances[0].Status != "invalid app port" || instances[0].AppPort != "" || instances[1].Status != "stopped" || instances[1].AppPort != "5200" || instances[1].ProxyPort != "5201" {
		t.Fatalf("unexpected entries: %#v", instances)
	}
}

func TestProbeClientPolicies(t *testing.T) {
	client := localProbeClient()
	if client.Timeout != time.Second || client.CheckRedirect == nil {
		t.Fatal("probe client missing timeout/redirect policy")
	}
	if err := client.CheckRedirect(httptest.NewRequest(http.MethodGet, "http://example.invalid", nil), nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect policy = %v", err)
	}
	if tr, ok := client.Transport.(*http.Transport); !ok || tr.Proxy != nil {
		t.Fatal("probe transport must disable proxies")
	}
}

func TestProbeFailureDistinctFromNoInstances(t *testing.T) {
	if got, err := loadGustInstances(context.Background(), func(context.Context) ([]probe.Instance, error) { return nil, errors.New("missing") }, localProbeClient()); len(got) != 0 || err == "" {
		t.Fatalf("expected discovery failure, got %#v, %q", got, err)
	}
	if got, err := loadGustInstances(context.Background(), scanner(), localProbeClient()); len(got) != 0 || err != "" {
		t.Fatalf("expected empty entries without error: %#v, %q", got, err)
	}
}

func TestDiscoveryHonorsRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := loadGustInstances(ctx, func(ctx context.Context) ([]probe.Instance, error) { <-ctx.Done(); return nil, ctx.Err() }, nil)
	if err == "" || time.Since(start) > time.Second {
		t.Fatalf("expected prompt discovery cancellation, got %q after %s", err, time.Since(start))
	}
}

func TestDisplayWorkdir(t *testing.T) {
	for _, tc := range []struct{ workdir, home, want string }{
		{"/home/pi/ced", "/home/pi", "ced"},
		{"/home/pi/wit/v2", "/home/pi", "wit/v2"},
		{"/home/pi", "/home/pi", "pi"},
		{"/home/pix/ced", "/home/pi", "/home/pix/ced"},
		{"/other/ced", "/home/pi", "/other/ced"},
		{"/home/pi/ced", "", "/home/pi/ced"},
	} {
		if got := displayWorkdir(tc.workdir, tc.home); got != tc.want {
			t.Errorf("displayWorkdir(%q, %q) = %q, want %q", tc.workdir, tc.home, got, tc.want)
		}
	}
}

func TestSafeTailscaleURLAndTemplateEscaping(t *testing.T) {
	if safeTailscaleURL("javascript:alert(1)") != "" || safeTailscaleURL("https://user:pass@example.ts.net") != "" || safeTailscaleURL("https://example.com") != "" {
		t.Fatal("unsafe URL accepted")
	}
	if safeTailscaleURL("https://example.ts.net/path?a=1&b=2") == "" {
		t.Fatal("safe HTTPS URL rejected")
	}
	var out bytes.Buffer
	component := views.HomePage([]views.Instance{{Title: "<script>alert(1)</script>", Workdir: "/home/pi/ced", TailscaleURL: "https://safe.example.ts.net/?a=1&b=2", AppPort: "5200", State: "running", Status: "responding", Alive: true}}, "")
	if err := component.Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "<script>alert") || !strings.Contains(got, "&lt;script&gt;") || !strings.Contains(got, "&amp;") || !strings.Contains(got, `class="server"`) || !strings.Contains(got, `title="/home/pi/ced"`) {
		t.Fatalf("unescaped template data: %s", got)
	}
	if !strings.Contains(got, ".server.dev { border-color: light-dark(#15803d, #4ade80);") || !strings.Contains(got, "color: light-dark(#166534, #bbf7d0);") || !strings.Contains(got, "border: 1px solid light-dark(#2563eb, #93c5fd);") || !strings.Contains(got, "color: light-dark(#1e40af, #bfdbfe);") || !strings.Contains(got, "font-size: 1.25rem;") {
		t.Fatalf("missing dev green/non-dev blue styling or larger titles: %s", got)
	}
	card := got[strings.Index(got, `class="server"`):]
	if !strings.Contains(card, `<a href="https://safe.example.ts.net/?a=1&amp;b=2">`) || !strings.Contains(card, `class="server-arrow"`) || !strings.Contains(card, `class="running-dot" role="img" aria-label="Running"`) || strings.Contains(card, "running ·") || !strings.Contains(card, `class="port-number">5200</span>`) || strings.Index(card, "</a>") < strings.Index(card, "App:") {
		t.Fatalf("expected entire linked card with arrow: %s", card)
	}
	out.Reset()
	if err := views.HomePage([]views.Instance{{Title: "ced", Workdir: "/home/pi/ced"}}, "").Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `class="server offline"`) || !strings.Contains(out.String(), "<h3>ced </h3>") || strings.Contains(out.String(), `class="server-proxy-icon"`) || strings.Contains(out.String(), `class="server-arrow"`) || strings.Contains(out.String(), `<a href=`) {
		t.Fatalf("expected compact offline card with short title: %s", out.String())
	}
	out.Reset()
	if err := views.HomePage([]views.Instance{{Title: "dev", AppPort: "5201", ProxyPort: "5202", State: "running", Status: "responding", TailscaleURL: "https://safe.example.ts.net:5202/"}}, "").Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `class="server offline dev"`) || !strings.Contains(out.String(), `class="running-dot" role="img" aria-label="Running"`) || !strings.Contains(out.String(), `dev <svg class="server-proxy-icon"`) || !strings.Contains(out.String(), `aria-label="Development proxy"`) || !strings.Contains(out.String(), `class="server-arrow"`) || !strings.Contains(out.String(), `class="port-number">5201</span>`) || !strings.Contains(out.String(), `class="port-number">5202</span>`) || strings.Contains(out.String(), "running ·") {
		t.Fatalf("expected proxy icon next to title on linked card: %s", out.String())
	}
}
