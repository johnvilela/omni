package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("synthetic transport failure")
}

// TestTransportErrorsHideSecrets (SEC-05): a failing transport's error prints
// the request URL; the bot token and the gemini key must not be in it.
func TestTransportErrorsHideSecrets(t *testing.T) {
	tg := NewTelegram("http://example.invalid", "AUDIT_BOT_SECRET")
	tg.client = &http.Client{Transport: failingTransport{}}
	_, err := tg.GetMe(context.Background())
	if err == nil || strings.Contains(err.Error(), "AUDIT_BOT_SECRET") {
		t.Fatalf("GetMe error leaks the token: %v", err)
	}
	if err := tg.upload(context.Background(), "sendDocument", "document", 1, "x", strings.NewReader("x")); err == nil || strings.Contains(err.Error(), "AUDIT_BOT_SECRET") {
		t.Fatalf("upload error leaks the token: %v", err)
	}

	old := answerHTTP
	answerHTTP = &http.Client{Transport: failingTransport{}}
	t.Cleanup(func() { answerHTTP = old })
	t.Setenv("OMNI_GEMINI_API", "http://example.invalid")
	_, _, err = askAPI(context.Background(), "gemini", "AUDIT_LLM_SECRET_KEY", "hi")
	if err == nil || strings.Contains(err.Error(), "AUDIT_LLM_SECRET_KEY") {
		t.Fatalf("askAPI error leaks the key: %v", err)
	}
	oldLLM := llmHTTP
	llmHTTP = &http.Client{Transport: failingTransport{}}
	t.Cleanup(func() { llmHTTP = oldLLM })
	if err := validateLLMKey(context.Background(), "gemini", "AUDIT_LLM_SECRET_KEY"); err == nil || strings.Contains(err.Error(), "AUDIT_LLM_SECRET_KEY") {
		t.Fatalf("validateLLMKey error leaks the key: %v", err)
	}
}

// TestGeminiKeyInHeader: the key travels as x-goog-api-key, never as ?key=.
func TestGeminiKeyInHeader(t *testing.T) {
	var gotURL, gotHeader string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL, gotHeader = r.URL.String(), r.Header.Get("x-goog-api-key")
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`))
	}))
	defer fake.Close()
	t.Setenv("OMNI_GEMINI_API", fake.URL)
	if _, _, err := askAPI(context.Background(), "gemini", "K1234567890", "hi"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotURL, "K1234567890") || gotHeader != "K1234567890" {
		t.Fatalf("url %q header %q", gotURL, gotHeader)
	}
	req, _ := modelsRequest(context.Background(), "gemini", "K1234567890")
	if strings.Contains(req.URL.String(), "K1234567890") || req.Header.Get("x-goog-api-key") != "K1234567890" {
		t.Fatalf("models url %q", req.URL)
	}
}

// TestRunCLIDoesNotLogStderr (SEC-05): a failing vendor cli's stderr echoes
// the composed prompt; the log gets the last line only.
func TestRunCLIDoesNotLogStderr(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	registerSecret("AUDIT_SECRET_VALUE")
	_, err := runCLI(context.Background(), "", 5*time.Second, "/bin/sh", "-c", "printf 'AUDIT_PRIVATE_PROMPT\\nfailed AUDIT_SECRET_VALUE\\n' >&2; exit 1")
	if err == nil {
		t.Fatal("expected failure")
	}
	if strings.Contains(buf.String(), "AUDIT_PRIVATE_PROMPT") || strings.Contains(err.Error(), "AUDIT_PRIVATE_PROMPT") {
		t.Fatalf("prompt leaked: log %q err %v", buf.String(), err)
	}
	if strings.Contains(buf.String(), "AUDIT_SECRET_VALUE") || strings.Contains(err.Error(), "AUDIT_SECRET_VALUE") {
		t.Fatalf("secret leaked: log %q err %v", buf.String(), err)
	}
	if !strings.Contains(err.Error(), "failed") {
		t.Fatalf("last line missing from error: %v", err)
	}
}

func TestRedactErrUnwraps(t *testing.T) {
	registerSecret("SECRET_VALUE_1")
	err := redactErr(errors.Join(context.DeadlineExceeded, errors.New("x SECRET_VALUE_1 y")))
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "SECRET_VALUE_1") {
		t.Fatalf("%v", err)
	}
}
