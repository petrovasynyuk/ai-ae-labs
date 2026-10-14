package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func request(t *testing.T, client *http.Client, method, url, body string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(data)
}

func makeRunBody(appName, userID, sessionID, message string) string {
	return fmt.Sprintf(`{"appName":%q,"userId":%q,"sessionId":%q,"newMessage":{"role":"user","parts":[{"text":%q}]}}`,
		appName, userID, sessionID, message)
}

func createSession(t *testing.T, client *http.Client, serverURL, appName, userID, sessionID string) string {
	t.Helper()
	path := fmt.Sprintf("/api/apps/%s/users/%s/sessions/%s", appName, userID, sessionID)
	status, _, body := request(t, client, "POST", serverURL+path, "{}")
	if status != http.StatusOK {
		t.Fatalf("create session %s: %d %s", sessionID, status, body)
	}
	return path
}

func TestADKServiceWithoutKey(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	svc, err := newService()
	if err != nil {
		t.Fatal(err)
	}
	checkADKService(t, svc)
}

func TestNewGraphNilRegistry(t *testing.T) {
	_, err := newGraph(nil)
	if err == nil {
		t.Fatal("expected error for nil registry, got nil")
	}
}

func TestListenAddress(t *testing.T) {
	for _, tc := range []struct{ port, want string }{
		{"", "127.0.0.1:8080"},
		{"8080", "0.0.0.0:8080"},
		{"9090", "0.0.0.0:9090"},
		{"0", ""}, {"65536", ""}, {"bad", ""},
	} {
		got, err := listenAddress(tc.port)
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Fatalf("listenAddress(%q) = %q, %v", tc.port, got, err)
		}
	}
}

func TestValidatePprofAddr(t *testing.T) {
	for _, tc := range []struct {
		addr    string
		wantErr bool
	}{
		{"", false},
		{"127.0.0.1:6060", false},
		{"127.0.0.1:0", false},
		{"0.0.0.0:6060", true},
		{"192.168.1.10:6060", true},
		{"localhost:6060", true},
		{"invalid", true},
		{"127.0.0.1:99999", true},
	} {
		err := validatePprofAddr(tc.addr)
		if (err != nil) != tc.wantErr {
			t.Errorf("validatePprofAddr(%q) error = %v, wantErr = %v", tc.addr, err, tc.wantErr)
		}
	}
}

func TestPprofServerIsolated(t *testing.T) {
	server, listener, err := newPprofServer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("newPprofServer: %v", err)
	}
	defer listener.Close()
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Close() }()

	pprofURL := "http://" + listener.Addr().String()
	resp, err := http.Get(pprofURL + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET pprof index: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET pprof index status = %d, want 200", resp.StatusCode)
	}

	// Verify main service does NOT mount /debug/pprof/
	svc, err := newService()
	if err != nil {
		t.Fatal(err)
	}
	mainServer := httptest.NewServer(svc)
	defer mainServer.Close()

	respMain, err := http.Get(mainServer.URL + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET main server pprof: %v", err)
	}
	defer respMain.Body.Close()
	if respMain.StatusCode != http.StatusNotFound {
		t.Fatalf("main server mounted pprof: status = %d, want 404", respMain.StatusCode)
	}
}

type observedListener struct {
	net.Listener
	closed chan struct{}
	once   sync.Once
}

func (l *observedListener) Close() error {
	err := l.Listener.Close()
	l.once.Do(func() { close(l.closed) })
	return err
}

func TestShutdown(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "drains active request"
		if timeout {
			name = "timeout closes active request"
		}
		t.Run(name, func(t *testing.T) {
			release := make(chan struct{})
			handlerDone := make(chan struct{})
			svc := &service{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handlerDone)
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				select {
				case <-release:
					if _, err := io.WriteString(w, "finished"); err != nil {
						t.Error(err)
					}
				case <-r.Context().Done():
				}
			})}
			base, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			listener := &observedListener{Listener: base, closed: make(chan struct{})}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			t.Cleanup(func() { close(release) })
			done := make(chan error, 1)
			drain := 5 * time.Second
			if timeout {
				drain = 0
			}
			go func() { done <- svc.serve(ctx, listener, drain) }()
			client := &http.Client{Timeout: 10 * time.Second}
			resp, err := client.Get("http://" + listener.Addr().String() + "/api/held")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			cancel()
			select {
			case <-listener.closed:
			case <-time.After(10 * time.Second):
				t.Fatal("listener did not close")
			}
			if !svc.draining.Load() {
				t.Fatal("readiness not cleared")
			}
			if !timeout {
				select {
				case err := <-done:
					t.Fatalf("returned before active request completed: %v", err)
				default:
				}
				release <- struct{}{}
				body, err := io.ReadAll(resp.Body)
				if err != nil || string(body) != "finished" {
					t.Fatalf("active response = %q, %v", body, err)
				}
			}
			select {
			case err := <-done:
				if timeout && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("timeout error = %v", err)
				}
				if !timeout && err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("shutdown did not finish")
			}
			select {
			case <-handlerDone:
			case <-time.After(10 * time.Second):
				t.Fatal("active handler was not cancelled")
			}
		})
	}
}

func TestServeFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	svc := &service{handler: http.NotFoundHandler()}
	if err := svc.serve(t.Context(), listener, time.Second); err == nil {
		t.Fatal("closed listener must fail")
	}
}
func checkADKService(t *testing.T, svc *service) {
	t.Helper()
	server := httptest.NewServer(svc)
	defer server.Close()

	// Initial health & readiness checks
	for _, path := range []string{"/health", "/readyz"} {
		status, _, body := request(t, server.Client(), "GET", server.URL+path, "")
		if status != http.StatusOK || body != "OK\n" {
			t.Fatalf("%s: %d %s", path, status, body)
		}
	}

	// 1. Table-driven SSE route tests with distinct sessions for independent scenarios
	type sseRouteTest struct {
		name       string
		sessionID  string
		message    string
		wantStatus int
		wantHeader string
		wantTexts  []string
		checkState string
	}

	routes := []sseRouteTest{
		{
			name:       "refund route opens new case",
			sessionID:  "session-refund",
			message:    "txn-2026-07-118845 A-114",
			wantStatus: http.StatusOK,
			wantHeader: "text/event-stream",
			wantTexts:  []string{"data:", "refund:last_case_id", "rc-txn-2026-07-118845-A-114", "pending"},
			checkState: "rc-txn-2026-07-118845-A-114",
		},
		{
			name:       "refund route duplicate case returns already_open",
			sessionID:  "session-refund",
			message:    "txn-2026-07-118845 A-114",
			wantStatus: http.StatusOK,
			wantHeader: "text/event-stream",
			wantTexts:  []string{"data:", "refund:last_case_id", "rc-txn-2026-07-118845-A-114", "already_open"},
			checkState: "rc-txn-2026-07-118845-A-114",
		},
		{
			name:       "status route checks existing case from registry in distinct session",
			sessionID:  "session-status",
			message:    "перевір статус rc-txn-2026-07-118845-A-114",
			wantStatus: http.StatusOK,
			wantHeader: "text/event-stream",
			wantTexts:  []string{"data:", "rc-txn-2026-07-118845-A-114", "уже в реєстрі", "pending"},
		},
		{
			name:       "out of domain route refuses irrelevant message in distinct session",
			sessionID:  "session-ood",
			message:    "привіт, яка сьогодні погода?",
			wantStatus: http.StatusOK,
			wantHeader: "text/event-stream",
			wantTexts:  []string{"data:", "поза цим доменом"},
		},
	}

	createdSessions := make(map[string]bool)
	for _, tc := range routes {
		t.Run(tc.name, func(t *testing.T) {
			if !createdSessions[tc.sessionID] {
				createSession(t, server.Client(), server.URL, "first_graph_agent", "learner", tc.sessionID)
				createdSessions[tc.sessionID] = true
			}
			body := makeRunBody("first_graph_agent", "learner", tc.sessionID, tc.message)
			status, headers, respBody := request(t, server.Client(), "POST", server.URL+"/api/run_sse", body)
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", status, tc.wantStatus, respBody)
			}
			if ct := headers.Get("Content-Type"); !strings.HasPrefix(ct, tc.wantHeader) {
				t.Fatalf("Content-Type = %q, want prefix %q", ct, tc.wantHeader)
			}
			for _, text := range tc.wantTexts {
				if !strings.Contains(respBody, text) {
					t.Fatalf("missing %q in SSE response: %s", text, respBody)
				}
			}
			if tc.checkState != "" {
				sessPath := fmt.Sprintf("/api/apps/first_graph_agent/users/learner/sessions/%s", tc.sessionID)
				sStatus, _, sBody := request(t, server.Client(), "GET", server.URL+sessPath, "")
				if sStatus != http.StatusOK || !strings.Contains(sBody, tc.checkState) {
					t.Fatalf("persisted session state %s = %d %s, want %q", sessPath, sStatus, sBody, tc.checkState)
				}
			}
		})
	}

	// 2. Table-driven negative test scenarios
	negativeCases := []struct {
		name       string
		body       string
		wantStatus int
		wantText   string
	}{
		{
			name:       "missing session returns 404",
			body:       makeRunBody("first_graph_agent", "learner", "nonexistent-session", "txn-2026-07-118845 A-114"),
			wantStatus: http.StatusNotFound,
			wantText:   "failed to find",
		},
		{
			name:       "unknown appName returns 404",
			body:       makeRunBody("missing_agent", "learner", "session-refund", "txn-2026-07-118845 A-114"),
			wantStatus: http.StatusNotFound,
			wantText:   "failed to find",
		},
		{
			name:       "invalid JSON returns 400",
			body:       `{"appName": "first_graph_agent", invalid`,
			wantStatus: http.StatusBadRequest,
			wantText:   "failed to decode",
		},
		{
			name:       "node error after SSE starts returns event error",
			body:       makeRunBody("first_graph_agent", "learner", "session-refund", "txn-2026-07-118845 Z-999"),
			wantStatus: http.StatusOK,
			wantText:   "event: error",
		},
	}

	for _, tc := range negativeCases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, respBody := request(t, server.Client(), "POST", server.URL+"/api/run_sse", tc.body)
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", status, tc.wantStatus, respBody)
			}
			if !strings.Contains(respBody, tc.wantText) {
				t.Fatalf("missing %q in response: %s", tc.wantText, respBody)
			}
		})
	}

	// 3. Draining and liveness checks
	t.Run("draining rejects new requests while health stays ok", func(t *testing.T) {
		svc.draining.Store(true)
		for _, path := range []string{"/readyz", "/api/run_sse"} {
			status, _, body := request(t, server.Client(), "POST", server.URL+path, makeRunBody("first_graph_agent", "learner", "session-refund", "test"))
			if status != http.StatusServiceUnavailable {
				t.Fatalf("drain %s: %d %s", path, status, body)
			}
		}
		status, _, body := request(t, server.Client(), "GET", server.URL+"/health", "")
		if status != http.StatusOK || body != "OK\n" {
			t.Fatalf("liveness during drain = %d %s", status, body)
		}
	})
}
