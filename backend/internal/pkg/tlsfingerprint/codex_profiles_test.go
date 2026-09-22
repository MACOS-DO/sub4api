//go:build unit

package tlsfingerprint

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"net"
	"strconv"
	"strings"
	"testing"

	utls "github.com/refraction-networking/utls"
)

// 基线来自官方 codex-cli 0.155.1 (x86_64-unknown-linux-musl) 实测采集：
//   - HTTP /responses：native-tls + 静态 OpenSSL 3.6.3，JA3 0b85eb0d4981e69064e40753e4f0ac5f
//   - Responses WebSocket：rustls 0.23，扩展顺序每连接随机
var (
	codexOpenSSLBaselineCiphers = []uint16{
		4866, 4867, 4865, 49196, 49200, 159, 52393, 52392, 52394, 49195, 49199, 158,
		49188, 49192, 107, 49187, 49191, 103, 49162, 49172, 57, 49161, 49171, 51,
		157, 156, 61, 60, 53, 47,
	}
	codexOpenSSLBaselineExts = []uint16{65281, 0, 11, 10, 35, 22, 23, 13, 43, 45, 51}
	codexOpenSSLBaselineJA3  = "0b85eb0d4981e69064e40753e4f0ac5f"

	codexRustlsBaselineCiphers = []uint16{4866, 4865, 4867, 49196, 49195, 52393, 49200, 49199, 52392, 255}
	codexRustlsBaselineExts    = []uint16{0, 5, 10, 11, 13, 23, 35, 43, 45, 51}
	codexRustlsBaselineGroups  = []uint16{4588, 29, 23, 24}
	codexRustlsBaselineSigAlgs = []uint16{1283, 1027, 1539, 2055, 2054, 2053, 2052, 1537, 1281, 1025}

	codexBaselineGroups  = []uint16{4588, 29, 23, 30, 24, 25, 256, 257}
	codexBaselineSigAlgs = []uint16{
		2309, 2310, 2308, 1027, 1283, 1539, 2055, 2056, 2074, 2075, 2076,
		2057, 2058, 2059, 2052, 2053, 2054, 1025, 1281, 1537,
		771, 769, 770, 1026, 1282, 1538,
	}
	codexBaselineKeyShares = []uint16{4588, 29}
)

func marshalCodexProfile(t *testing.T, profile *Profile) *parsedClientHello {
	t.Helper()
	spec := buildClientHelloSpecFromProfile(profile)
	conn, _ := net.Pipe()
	defer conn.Close()
	uconn := utls.UClient(conn, &utls.Config{ServerName: "chatgpt.com"}, utls.HelloCustom)
	if err := uconn.ApplyPreset(spec); err != nil {
		t.Fatalf("apply preset: %v", err)
	}
	if err := uconn.MarshalClientHelloNoECH(); err != nil {
		t.Fatalf("marshal client hello: %v", err)
	}
	raw := uconn.HandshakeState.Hello.Raw
	if len(raw) == 0 || raw[0] != 0x01 {
		t.Fatalf("unexpected client hello raw prefix: %x", raw[:min(4, len(raw))])
	}
	hello, err := parseTestClientHello(raw[4:])
	if err != nil {
		t.Fatalf("parse client hello: %v", err)
	}
	return hello
}

func TestCodexOpenSSLProfileMatchesOfficialBaseline(t *testing.T) {
	hello := marshalCodexProfile(t, CodexOpenSSLProfile())

	assertUint16Slice(t, "cipher_suites", hello.cipherSuites, codexOpenSSLBaselineCiphers)
	assertUint16Slice(t, "extension_order", hello.extensionOrder, codexOpenSSLBaselineExts)
	assertUint16Slice(t, "supported_groups", hello.supportedGroups, codexBaselineGroups)
	assertUint16Slice(t, "signature_algorithms", hello.sigAlgs, codexBaselineSigAlgs)
	assertUint16Slice(t, "key_share_groups", hello.keyShareGroups, codexBaselineKeyShares)
	if len(hello.alpn) != 0 {
		t.Fatalf("HTTP OpenSSL profile must not send ALPN, got %v", hello.alpn)
	}
	if hello.legacyVersion != 0x0303 {
		t.Fatalf("legacy_version = %#x, want 0x0303", hello.legacyVersion)
	}
	if hello.sessionIDLen != 32 {
		t.Fatalf("session_id len = %d, want 32", hello.sessionIDLen)
	}
	if got := hello.ja3Hash(); got != codexOpenSSLBaselineJA3 {
		t.Fatalf("JA3 = %s, want %s (raw=%s)", got, codexOpenSSLBaselineJA3, hello.ja3Raw())
	}
}

func TestCodexRustlsProfileMatchesOfficialBaselineAndRandomizesExtensionOrder(t *testing.T) {
	first := marshalCodexProfile(t, CodexRustlsProfile())
	assertUint16Slice(t, "cipher_suites", first.cipherSuites, codexRustlsBaselineCiphers)
	assertUint16Slice(t, "supported_groups", first.supportedGroups, codexRustlsBaselineGroups)
	assertUint16Slice(t, "signature_algorithms", first.sigAlgs, codexRustlsBaselineSigAlgs)
	assertUint16Slice(t, "key_share_groups", first.keyShareGroups, codexBaselineKeyShares)
	assertUint16Slice(t, "extension_set", sortedUint16(first.extensionOrder), sortedUint16(codexRustlsBaselineExts))
	if len(first.alpn) != 0 {
		t.Fatalf("WS rustls profile must not send ALPN, got %v", first.alpn)
	}

	// rustls randomizes order per connection; ensure we actually vary while keeping the set stable.
	orderChanged := false
	base := first.extensionOrder
	for i := 0; i < 8 && !orderChanged; i++ {
		next := marshalCodexProfile(t, CodexRustlsProfile())
		assertUint16Slice(t, "extension_set", sortedUint16(next.extensionOrder), sortedUint16(codexRustlsBaselineExts))
		if !equalUint16(next.extensionOrder, base) {
			orderChanged = true
		}
	}
	if !orderChanged {
		t.Fatal("expected rustls profile extension order to vary across connections")
	}
}

func TestProfileIdentityDistinguishesProfiles(t *testing.T) {
	opensslID := ProfileIdentity(CodexOpenSSLProfile())
	rustlsID := ProfileIdentity(CodexRustlsProfile())
	if opensslID == "" || rustlsID == "" {
		t.Fatal("profile identity must not be empty")
	}
	if opensslID == rustlsID {
		t.Fatal("different profiles must have different identities")
	}
	again := ProfileIdentity(CodexOpenSSLProfile())
	if opensslID != again {
		t.Fatalf("profile identity must be deterministic: %s != %s", opensslID, again)
	}
}

type parsedClientHello struct {
	legacyVersion  uint16
	sessionIDLen   int
	cipherSuites   []uint16
	compression    []uint8
	extensionOrder []uint16
	supportedGroups []uint16
	pointFormats   []uint8
	alpn           []string
	sigAlgs        []uint16
	keyShareGroups []uint16
}

func parseTestClientHello(b []byte) (*parsedClientHello, error) {
	h := &parsedClientHello{}
	h.legacyVersion = binary.BigEndian.Uint16(b[0:2])
	p := 34
	h.sessionIDLen = int(b[p])
	p++
	p += h.sessionIDLen
	csLen := int(binary.BigEndian.Uint16(b[p : p+2]))
	p += 2
	for i := 0; i < csLen; i += 2 {
		h.cipherSuites = append(h.cipherSuites, binary.BigEndian.Uint16(b[p+i:p+i+2]))
	}
	p += csLen
	compLen := int(b[p])
	p++
	h.compression = append(h.compression, b[p:p+compLen]...)
	p += compLen
	if p+2 > len(b) {
		return h, nil
	}
	extLen := int(binary.BigEndian.Uint16(b[p : p+2]))
	p += 2
	end := p + extLen
	if end > len(b) {
		end = len(b)
	}
	for p+4 <= end {
		typ := binary.BigEndian.Uint16(b[p : p+2])
		l := int(binary.BigEndian.Uint16(b[p+2 : p+4]))
		p += 4
		if p+l > end {
			l = end - p
		}
		data := b[p : p+l]
		p += l
		h.extensionOrder = append(h.extensionOrder, typ)
		switch typ {
		case 10:
			if len(data) >= 2 {
				n := int(binary.BigEndian.Uint16(data[0:2]))
				for i := 2; i+2 <= 2+n && i+2 <= len(data); i += 2 {
					h.supportedGroups = append(h.supportedGroups, binary.BigEndian.Uint16(data[i:i+2]))
				}
			}
		case 11:
			if len(data) >= 1 {
				n := int(data[0])
				if 1+n <= len(data) {
					h.pointFormats = append(h.pointFormats, data[1:1+n]...)
				}
			}
		case 16:
			if len(data) >= 2 {
				n := int(binary.BigEndian.Uint16(data[0:2]))
				q := 2
				for q < 2+n && q < len(data) {
					sl := int(data[q])
					q++
					if q+sl > len(data) {
						break
					}
					h.alpn = append(h.alpn, string(data[q:q+sl]))
					q += sl
				}
			}
		case 13:
			if len(data) >= 2 {
				n := int(binary.BigEndian.Uint16(data[0:2]))
				for i := 2; i+2 <= 2+n && i+2 <= len(data); i += 2 {
					h.sigAlgs = append(h.sigAlgs, binary.BigEndian.Uint16(data[i:i+2]))
				}
			}
		case 51:
			if len(data) >= 2 {
				n := int(binary.BigEndian.Uint16(data[0:2]))
				q := 2
				for q+4 <= 2+n && q+4 <= len(data) {
					g := binary.BigEndian.Uint16(data[q : q+2])
					kl := int(binary.BigEndian.Uint16(data[q+2 : q+4]))
					h.keyShareGroups = append(h.keyShareGroups, g)
					q += 4 + kl
				}
			}
		}
	}
	return h, nil
}

func (h *parsedClientHello) ja3Raw() string {
	ciphers := make([]string, 0, len(h.cipherSuites))
	for _, c := range h.cipherSuites {
		if isGREASEValue(c) {
			continue
		}
		ciphers = append(ciphers, strconv.Itoa(int(c)))
	}
	exts := make([]string, 0, len(h.extensionOrder))
	for _, e := range h.extensionOrder {
		if isGREASEValue(e) {
			continue
		}
		exts = append(exts, strconv.Itoa(int(e)))
	}
	groups := make([]string, 0, len(h.supportedGroups))
	for _, g := range h.supportedGroups {
		if isGREASEValue(g) {
			continue
		}
		groups = append(groups, strconv.Itoa(int(g)))
	}
	pf := make([]string, 0, len(h.pointFormats))
	for _, f := range h.pointFormats {
		pf = append(pf, strconv.Itoa(int(f)))
	}
	return strconv.Itoa(int(h.legacyVersion)) + "," + strings.Join(ciphers, "-") + "," + strings.Join(exts, "-") + "," +
		strings.Join(groups, "-") + "," + strings.Join(pf, "-")
}

func (h *parsedClientHello) ja3Hash() string {
	sum := md5.Sum([]byte(h.ja3Raw()))
	return hex.EncodeToString(sum[:])
}

func assertUint16Slice(t *testing.T, name string, got, want []uint16) {
	t.Helper()
	if !equalUint16(got, want) {
		t.Fatalf("%s mismatch:\n got %v\nwant %v", name, got, want)
	}
}

func equalUint16(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortedUint16(in []uint16) []uint16 {
	out := append([]uint16(nil), in...)
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
