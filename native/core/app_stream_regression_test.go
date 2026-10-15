package core

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeStreamMP4QueryAndRanges(t *testing.T) {
	data := []byte("\x00\x00\x00\x18ftypisomsynthetic-mp4-body")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("media transfer must preserve byte ranges")
		}
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "video.mp4", time.Time{}, bytes.NewReader(data))
	}))
	defer upstream.Close()
	engine, err := newNativeEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := newNativeStreamServer(engine.downloader)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.server.Close()
	for _, suffix := range []string{"/video.mp4?token=hls", "/video?signature=m3u8", "/hls-token/video.mp4"} {
		t.Run(suffix, func(t *testing.T) {
			address, token := stream.nativeOpen(providerMedia{URL: upstream.URL + suffix})
			defer stream.nativeRelease(token)
			if !strings.HasSuffix(address, ".mp4") {
				t.Fatalf("MP4 mislabeled: %s", address)
			}
			req, _ := http.NewRequest(http.MethodGet, address, nil)
			req.Header.Set("Range", "bytes=4-11")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || resp.StatusCode != 206 || !bytes.Equal(body, data[4:12]) || resp.Header.Get("Content-Range") == "" {
				t.Fatalf("range corrupted: status=%d body=%q err=%v", resp.StatusCode, body, err)
			}
		})
	}
}

func TestNativeStreamEncryptedMediaDecodes(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg required")
	}
	source := t.TempDir()
	key := []byte("0123456789abcdef")
	keyPath := filepath.Join(source, "test.key")
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}
	keyInfo := filepath.Join(source, "key-info.txt")
	if err := os.WriteFile(keyInfo, []byte("denied.key\n"+keyPath+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	run := func(t *testing.T, args ...string) {
		t.Helper()
		if output, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v\n%s", err, output)
		}
	}
	clear := filepath.Join(source, "clear.mp4")
	run(t, "-v", "error", "-y", "-f", "lavfi", "-i", "color=c=black:s=160x90:r=10", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "3", "-c:v", "libx264", "-threads", "1", "-g", "10", "-pix_fmt", "yuv420p", "-c:a", "aac", "-movflags", "+faststart", clear)
	run(t, "-v", "error", "-y", "-i", clear, "-c", "copy", "-hls_time", "1", "-hls_list_size", "0", "-hls_key_info_file", keyInfo, "-hls_segment_filename", filepath.Join(source, "seg-%d.ts"), filepath.Join(source, "index.m3u8"))
	run(t, "-v", "error", "-y", "-i", clear, "-c", "copy", "-movflags", "+faststart", "-encryption_scheme", "cenc-aes-ctr", "-encryption_key", hex.EncodeToString(key), "-encryption_kid", "11223344556677889900aabbccddeeff", filepath.Join(source, "encrypted.mp4"))
	files := http.FileServer(http.Dir(source))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/denied.key" {
			t.Error("resolved key should be served locally")
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/extensionless" {
			http.ServeFile(w, r, filepath.Join(source, "index.m3u8"))
			return
		}
		files.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	engine, err := newNativeEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := newNativeStreamServer(engine.downloader)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.server.Close()
	for _, tc := range []struct {
		name, path string
		hls        bool
	}{
		{"AES-HLS", "/index.m3u8", true}, {"extensionless-AES-HLS", "/extensionless", true}, {"CENC-MP4-query", "/encrypted.mp4?token=hls_m3u8", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			media := providerMedia{URL: upstream.URL + tc.path}
			if tc.hls {
				media.HLSKey = key
			} else {
				media.CENCKey = key
			}
			address, token := stream.nativeOpen(media)
			defer stream.nativeRelease(token)
			args := []string{"-v", "error", "-protocol_whitelist", "http,https,tcp,tls,crypto,data,file"}
			if tc.hls {
				args = append(args, "-allowed_extensions", "ALL")
			}
			if !tc.hls {
				args = append(args, "-decryption_key", hex.EncodeToString(key))
			}
			args = append(args, "-i", address, "-f", "null", "-")
			run(t, args...)
			if dll := os.Getenv("GUOAPP_TEST_LIBMPV"); dll != "" {
				python := os.Getenv("GUOAPP_TEST_PYTHON")
				if python == "" {
					python = "python"
				}
				script, err := filepath.Abs("../../scripts/check_windows_playback.py")
				if err != nil {
					t.Fatal(err)
				}
				contentKey := ""
				if !tc.hls {
					contentKey = hex.EncodeToString(key)
				}
				if output, err := exec.CommandContext(ctx, python, script, dll, address, contentKey).CombinedOutput(); err != nil {
					t.Fatalf("packaged libmpv: %v\n%s", err, output)
				} else {
					t.Log(string(output))
				}
			}
		})
	}
}
