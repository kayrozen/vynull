// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// browserAudioTypes maps the library FileType to a Content-Type for formats
// every desktop browser decodes natively and gaplessly. Anything else (AIFF,
// M4A/AAC whose priming offset varies between decoders, …) is transcoded to
// WAV through ffmpeg, the same decoder the analysis uses, so the browser
// position lines up with the beat grid and cue times.
var browserAudioTypes = map[string]string{
	"mp3":  "audio/mpeg",
	"wav":  "audio/wav",
	"flac": "audio/flac",
	"ogg":  "audio/ogg",
}

// audioTranscodeMu serialises ffmpeg transcodes so two requests for the same
// track (the browser often issues a probe plus a Range request) don't both
// encode into the cache.
var audioTranscodeMu sync.Mutex

// handleAudio streams a library track to the web UI player:
// GET|HEAD /api/audio/{trackID}[?transcode=1]. Range requests are honoured so
// the browser can seek. transcode=1 forces the ffmpeg WAV path even for
// natively playable formats, to compare against the analysis decode.
// Only tracks in the library are served, never arbitrary paths.
func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Library == nil {
		http.Error(w, "library not available", http.StatusServiceUnavailable)
		return
	}
	trackID := parseTrackIDFromPath(r.URL.Path, "/api/audio/")
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}
	t := s.Library.Track(trackID)
	if t == nil || t.FilePath == "" {
		http.Error(w, "track not found", http.StatusNotFound)
		return
	}
	if _, err := os.Stat(t.FilePath); err != nil {
		http.Error(w, "audio file missing", http.StatusNotFound)
		return
	}

	path := t.FilePath
	ctype, native := browserAudioTypes[strings.ToLower(t.FileType)]
	source := "direct"
	if !native || r.URL.Query().Get("transcode") == "1" {
		p, err := s.transcodedAudio(t.FilePath)
		if err != nil {
			log.Printf("api: audio transcode track %d: %v", trackID, err)
			http.Error(w, "transcode failed", http.StatusInternalServerError)
			return
		}
		path, ctype, source = p, "audio/wav", "transcoded"
	}

	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "audio file unreadable", http.StatusNotFound)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.Error(w, "audio file unreadable", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Audio-Source", source)
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// transcodedAudio returns a cached 16-bit WAV of src, creating it with ffmpeg
// on first use. The cache key includes size and mtime so an edited file is
// re-encoded.
func (s *Server) transcodedAudio(src string) (string, error) {
	fi, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	dir := s.CacheDir
	if dir == "" {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "audio")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	h := fnv.New32a()
	h.Write([]byte(src))
	key := fmt.Sprintf("%08x", h.Sum32())
	dst := filepath.Join(dir, fmt.Sprintf("%s_%d_%d.wav", key, fi.Size(), fi.ModTime().UnixNano()))

	audioTranscodeMu.Lock()
	defer audioTranscodeMu.Unlock()
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	tmp := dst + ".part"
	start := time.Now()
	out, err := exec.Command("ffmpeg", "-nostdin", "-y",
		"-i", src, "-vn", "-map", "0:a:0",
		"-c:a", "pcm_s16le", "-f", "wav",
		"-loglevel", "error", tmp,
	).CombinedOutput()
	if err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("ffmpeg: %s: %w", strings.TrimSpace(string(out)), err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", err
	}
	log.Printf("api: transcoded %s to WAV in %v", filepath.Base(src), time.Since(start).Round(time.Millisecond))
	return dst, nil
}
