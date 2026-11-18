package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoSendsTokenAndDecodesErrors(t *testing.T) {
	var gotAuth, gotUA, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotUA, gotPath = r.Header.Get("Authorization"), r.Header.Get("User-Agent"), r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": "The hostname is taken.",
			"errors":  map[string][]string{"hostname": {"The hostname is taken."}, "agent": {"Unknown agent."}},
		})
	}))
	defer srv.Close()

	c := New(srv.URL+"/api/v1/", "cgt_x", "terraform-provider-codigate/test")
	_, err := c.App(context.Background(), "app 1/2")
	if gotAuth != "Bearer cgt_x" || gotUA != "terraform-provider-codigate/test" {
		t.Fatalf("headers: %q %q", gotAuth, gotUA)
	}
	if gotPath != "/api/v1/apps/app%201%2F2" {
		t.Fatalf("path not escaped: %s", gotPath)
	}
	var e *Error
	if !errors.As(err, &e) || e.Status != 422 || !IsValidation(err) || IsNotFound(err) {
		t.Fatalf("err = %v", err)
	}
	want := "codigate API: The hostname is taken. (HTTP 422)\n  agent: Unknown agent.\n  hostname: The hostname is taken."
	if err.Error() != want {
		t.Fatalf("message:\n%s\nwant:\n%s", err.Error(), want)
	}
}

func TestErrorWithoutBodyUsesStatusText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := New(srv.URL, "t", "").Agent(context.Background(), "agt_1")
	if !IsNotFound(err) || !strings.Contains(err.Error(), "Not Found (HTTP 404)") {
		t.Fatalf("err = %v", err)
	}
}

func TestAppByHostname(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"app_1","hostname":"wiki.example.com"}]}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "t", "")
	a, err := c.AppByHostname(context.Background(), " Wiki.Example.com. ")
	if err != nil || a.ID != "app_1" {
		t.Fatalf("got %v, %v", a, err)
	}
	if _, err := c.AppByHostname(context.Background(), "other.example.com"); !IsNotFound(err) {
		t.Fatalf("missing app: %v", err)
	}
}

func TestUnexpectedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>`))
	}))
	defer srv.Close()
	_, err := New(srv.URL, "t", "").Workspace(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unexpected response from GET /workspace") {
		t.Fatalf("err = %v", err)
	}
}
