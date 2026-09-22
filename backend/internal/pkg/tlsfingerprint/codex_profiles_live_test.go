//go:build integration

package tlsfingerprint

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestCodexProfilesLiveHandshake 用真实 TLS 握手 + tls.peet.ws 交叉验证内置
// Codex 指纹：cipher list 必须逐项一致，扩展集合必须一致（rustls 顺序随机）。
// 需要 TLSFINGERPRINT_LIVE_TEST=1 且可访问公网。
func TestCodexProfilesLiveHandshake(t *testing.T) {
	if os.Getenv("TLSFINGERPRINT_LIVE_TEST") != "1" {
		t.Skip("set TLSFINGERPRINT_LIVE_TEST=1 to run live fingerprint verification")
	}
	cases := []struct {
		name    string
		profile *Profile
		ciphers []uint16
		exts    []uint16
	}{
		{
			name:    "codex_openssl",
			profile: CodexOpenSSLProfile(),
			ciphers: codexOpenSSLCipherSuites,
			exts:    codexOpenSSLExtensions,
		},
		{
			name:    "codex_rustls",
			profile: CodexRustlsProfile(),
			ciphers: codexRustlsCipherSuites,
			exts:    codexRustlsExtensions,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dialer := NewDialer(tc.profile, nil)
			client := &http.Client{
				Transport: &http.Transport{DialTLSContext: dialer.DialTLSContext},
				Timeout:   20 * time.Second,
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://tls.peet.ws/api/all", nil)
			if err != nil {
				t.Fatalf("create request: %v", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("live handshake failed: %v", err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			var fp FingerprintResponse
			if err := json.Unmarshal(body, &fp); err != nil {
				t.Fatalf("parse fingerprint response: %v body=%s", err, string(body))
			}
			t.Logf("JA3=%s JA4=%s", fp.TLS.JA3, fp.TLS.JA4)

			ciphers, exts := parseJA3Lists(t, fp.TLS.JA3)
			if got, want := joinUint16(ciphers), joinUint16(tc.ciphers); got != want {
				t.Fatalf("cipher suites mismatch:\n got %s\nwant %s", got, want)
			}
			if got, want := joinSortedUint16(exts), joinSortedUint16(tc.exts); got != want {
				t.Fatalf("extension set mismatch:\n got %s\nwant %s", got, want)
			}
		})
	}
}

func parseJA3Lists(t *testing.T, ja3 string) ([]uint16, []uint16) {
	t.Helper()
	parts := strings.Split(ja3, ",")
	if len(parts) < 3 {
		t.Fatalf("unexpected JA3 string: %q", ja3)
	}
	return parseUint16List(t, parts[1]), parseUint16List(t, parts[2])
}

func parseUint16List(t *testing.T, raw string) []uint16 {
	t.Helper()
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	fields := strings.Split(raw, "-")
	out := make([]uint16, 0, len(fields))
	for _, f := range fields {
		v, err := strconv.ParseUint(f, 10, 16)
		if err != nil {
			t.Fatalf("parse %q: %v", f, err)
		}
		out = append(out, uint16(v))
	}
	return out
}

func joinUint16(values []uint16) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = strconv.Itoa(int(v))
	}
	return strings.Join(parts, "-")
}

func joinSortedUint16(values []uint16) string {
	sorted := append([]uint16(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return fmt.Sprintf("%v", sorted)
}
