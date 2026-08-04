package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseDirectPlayRequestFromJSON(t *testing.T) {
	body := bytes.NewBufferString(`{"url":"https://example.invalid/file.nzb"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/play/nzb", body)
	req.Header.Set("Content-Type", "application/json")

	url, sourceName, libraryID, payload, err := parseDirectPlayRequest(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatalf("parseDirectPlayRequest returned error: %v", err)
	}
	if url != "https://example.invalid/file.nzb" {
		t.Fatalf("url = %q", url)
	}
	if sourceName != "" {
		t.Fatalf("sourceName = %q, want empty", sourceName)
	}
	if libraryID != "" {
		t.Fatalf("libraryID = %q, want empty", libraryID)
	}
	if len(payload) != 0 {
		t.Fatalf("payload length = %d, want 0", len(payload))
	}
}

func TestParseDirectPlayRequestFromJSONLibraryID(t *testing.T) {
	body := bytes.NewBufferString(`{"library_id":"movie:tt42:Movie.2024"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/play/nzb", body)
	req.Header.Set("Content-Type", "application/json")

	url, _, libraryID, payload, err := parseDirectPlayRequest(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatalf("parseDirectPlayRequest returned error: %v", err)
	}
	if url != "" || len(payload) != 0 {
		t.Fatalf("expected only library id, got url=%q payload=%d", url, len(payload))
	}
	if libraryID != "movie:tt42:Movie.2024" {
		t.Fatalf("libraryID = %q", libraryID)
	}
}

func TestParseDirectPlayRequestFromMultipartFile(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("url", "https://example.invalid/ignored.nzb"); err != nil {
		t.Fatalf("WriteField failed: %v", err)
	}
	part, err := writer.CreateFormFile("file", "local.nzb")
	if err != nil {
		t.Fatalf("CreateFormFile failed: %v", err)
	}
	if _, err := part.Write([]byte("<nzb></nzb>")); err != nil {
		t.Fatalf("part.Write failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/play/nzb", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	url, sourceName, _, payload, err := parseDirectPlayRequest(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatalf("parseDirectPlayRequest returned error: %v", err)
	}
	if url != "https://example.invalid/ignored.nzb" {
		t.Fatalf("url = %q", url)
	}
	if sourceName != "local.nzb" {
		t.Fatalf("sourceName = %q", sourceName)
	}
	if string(payload) != "<nzb></nzb>" {
		t.Fatalf("payload = %q", string(payload))
	}
}

func TestHandleDirectPlayNZBMethodNotAllowed(t *testing.T) {
	srv := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/play/nzb", nil)
	rr := httptest.NewRecorder()

	srv.handleDirectPlayNZB(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestReadLimitedNZBRejectsEmptyPayload(t *testing.T) {
	if _, err := readLimitedNZB(bytes.NewBufferString("   "), 1024); err == nil {
		t.Fatal("expected empty payload to fail")
	}
}

func TestParseDirectPlayRequestRejectsEmptyJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/play/nzb", bytes.NewBufferString("{}"))
	req.Header.Set("Content-Type", "application/json")
	_, _, _, _, err := parseDirectPlayRequest(httptest.NewRecorder(), req)
	if err == nil {
		t.Fatal("expected error for missing URL and file")
	}
}

func TestDirectPlayResponseShape(t *testing.T) {
	resp := directPlayResponse{
		SessionID:       "direct-abc",
		PlayURL:         "/token/play/direct-abc",
		PlayPath:        "/token/play/direct-abc",
		ExternalPlayURL: "https://example/play/direct-abc",
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	if !bytes.Contains(data, []byte(`"session_id"`)) ||
		!bytes.Contains(data, []byte(`"play_url"`)) ||
		!bytes.Contains(data, []byte(`"play_path"`)) ||
		!bytes.Contains(data, []byte(`"external_play_url"`)) {
		t.Fatalf("unexpected JSON shape: %s", string(data))
	}
}
