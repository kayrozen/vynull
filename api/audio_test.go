// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/vynulldev/vynull/library"
)

func audioTestServer(t *testing.T, tracks ...*library.Track) *Server {
	t.Helper()
	lib := library.New()
	for _, tr := range tracks {
		lib.AddTrack(tr)
	}
	return &Server{Library: lib, CacheDir: t.TempDir()}
}

func TestHandleAudioRange(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.mp3")
	if err := os.WriteFile(p, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := audioTestServer(t, &library.Track{ID: 1, FilePath: p, FileType: "mp3"})

	req := httptest.NewRequest("GET", "/api/audio/1", nil)
	req.Header.Set("Range", "bytes=2-5")
	w := httptest.NewRecorder()
	s.handleAudio(w, req)
	if w.Code != 206 {
		t.Fatalf("range: code %d, want 206", w.Code)
	}
	if got := w.Body.String(); got != "2345" {
		t.Errorf("range body = %q, want 2345", got)
	}
	if ct := w.Header().Get("Content-Type"); ct != "audio/mpeg" {
		t.Errorf("content-type = %q", ct)
	}
	if src := w.Header().Get("X-Audio-Source"); src != "direct" {
		t.Errorf("source = %q, want direct", src)
	}
}

func TestHandleAudioRejects(t *testing.T) {
	s := audioTestServer(t,
		&library.Track{ID: 1, FilePath: filepath.Join(t.TempDir(), "gone.mp3"), FileType: "mp3"},
	)
	for _, c := range []struct {
		method, url string
		want        int
	}{
		{"GET", "/api/audio/", 400},
		{"GET", "/api/audio/99", 404}, // unknown track
		{"GET", "/api/audio/1", 404},  // file missing on disk
		{"POST", "/api/audio/1", 405},
	} {
		w := httptest.NewRecorder()
		s.handleAudio(w, httptest.NewRequest(c.method, c.url, nil))
		if w.Code != c.want {
			t.Errorf("%s %s: code %d, want %d", c.method, c.url, w.Code, c.want)
		}
	}
	if w := httptest.NewRecorder(); true {
		(&Server{}).handleAudio(w, httptest.NewRequest("GET", "/api/audio/1", nil))
		if w.Code != 503 {
			t.Errorf("no library: code %d, want 503", w.Code)
		}
	}
}

func TestHandleAudioTranscode(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "tone.aiff")
	if out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1", src).CombinedOutput(); err != nil {
		t.Fatalf("make fixture: %v: %s", err, out)
	}
	s := audioTestServer(t, &library.Track{ID: 1, FilePath: src, FileType: "aiff"})

	w := httptest.NewRecorder()
	s.handleAudio(w, httptest.NewRequest("GET", "/api/audio/1", nil))
	if w.Code != 200 {
		t.Fatalf("code %d: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Audio-Source") != "transcoded" || w.Header().Get("Content-Type") != "audio/wav" {
		t.Errorf("headers: %v", w.Header())
	}
	if b := w.Body.Bytes(); len(b) < 44 || string(b[:4]) != "RIFF" {
		t.Errorf("body is not a WAV (%d bytes)", len(b))
	}
	matches, _ := filepath.Glob(filepath.Join(s.CacheDir, "audio", "*.wav"))
	if len(matches) != 1 {
		t.Errorf("cache files = %v, want exactly one", matches)
	}
}
