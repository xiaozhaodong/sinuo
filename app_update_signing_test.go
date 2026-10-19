package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

func signedUpdateFixture(t *testing.T, sidecar string, statusCode int) (*signedGitHubProvider, []byte, *atomic.Int32) {
	t.Helper()
	payload := []byte("test update artifact")
	digest := sha256.Sum256(payload)
	var downloads atomic.Int32
	var base string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/xiaozhaodong/sinuo/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v99.0.0", "assets": []map[string]any{
				{"name": fmt.Sprintf("Sinuo-%s-%s.bin", runtime.GOOS, runtime.GOARCH), "browser_download_url": base + "/artifact"},
				{"name": "checksums.txt", "browser_download_url": base + "/checksums"},
			}})
		case "/checksums":
			_, _ = fmt.Fprintf(w, "%s  Sinuo-%s-%s.bin\n", hex.EncodeToString(digest[:]), runtime.GOOS, runtime.GOARCH)
		case "/artifact.sig":
			w.WriteHeader(statusCode)
			_, _ = io.WriteString(w, sidecar)
		case "/artifact":
			downloads.Add(1)
			_, _ = w.Write(payload)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	base = server.URL
	inner, err := github.New(github.Config{Repository: "xiaozhaodong/sinuo", BaseURL: base, ChecksumAsset: "checksums.txt", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return &signedGitHubProvider{inner: inner, client: server.Client()}, payload, &downloads
}

func TestAppUpdate_SignedProviderCheck(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		code       int
		signed     bool
	}{
		{"valid", base64.StdEncoding.EncodeToString(make([]byte, 64)), 200, true},
		{"missing", "", 404, false}, {"encoding", "not base64", 200, false},
		{"length", base64.StdEncoding.EncodeToString(make([]byte, 63)), 200, false},
		{"oversize", strings.Repeat("A", 1025), 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, _, requests := signedUpdateFixture(t, tc.body, tc.code)
			release, err := provider.Check(context.Background(), updater.CheckRequest{CurrentVersion: Version, Platform: runtime.GOOS, Arch: runtime.GOARCH})
			if err != nil || release == nil || release.Metadata["sinuo.signed"] != tc.signed {
				t.Fatalf("release=%+v err=%v", release, err)
			}
			if tc.signed {
				if !appUpdateVerifiedRelease(release) {
					t.Fatal("签名和校验值未装配")
				}
			} else {
				if release.Metadata["sinuo.signature_error"] == nil {
					t.Fatal("丢失签名错误原因")
				}
				if err := provider.Download(context.Background(), release, io.Discard, nil); err == nil {
					t.Fatal("未拒绝下载")
				}
			}
			if requests.Load() != 0 {
				t.Fatal("Check 或拒绝下载时请求了安装包")
			}
		})
	}
}

func TestAppUpdate_ProviderDownloadFailClosed(t *testing.T) {
	provider, _, requests := signedUpdateFixture(t, "", 404)
	valid := &updater.Verification{DigestAlgo: "sha256", Digest: make([]byte, 32), SignatureAlgo: "ed25519", Signature: make([]byte, 64)}
	for _, v := range []*updater.Verification{nil, {}, {Digest: valid.Digest}, {Signature: valid.Signature},
		{DigestAlgo: "sha256", Digest: valid.Digest, Signature: valid.Signature},
		{DigestAlgo: "sha512", Digest: valid.Digest, SignatureAlgo: "ed25519", Signature: valid.Signature}} {
		err := provider.Download(context.Background(), &updater.Release{Verification: v}, io.Discard, nil)
		if err == nil || err.Error() != "安装包缺少签名或校验值，已拒绝安装" {
			t.Fatalf("err=%v", err)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("拒绝安装前发送了下载请求")
	}
}

func TestAppUpdate_DownloadAndInstallRejectsWrongSigningKey(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("test update artifact"))
	provider, _, requests := signedUpdateFixture(t, base64.StdEncoding.EncodeToString(ed25519.Sign(private, digest[:])), 200)
	wrong, _, _ := ed25519.GenerateKey(rand.Reader)
	for _, tc := range []struct {
		name string
		key  []byte
		fail bool
	}{{"wrong", wrong, true}, {"correct", public, false}} {
		t.Run(tc.name, func(t *testing.T) {
			app, manager := updateTestManager(t)
			manager.baseBlockedReason = ""
			checker := updater.New(appUpdateTestHost{t})
			provider.onComplete, provider.onProgress = manager.downloadComplete, manager.downloadProgress
			if err := checker.Init(updater.Config{CurrentVersion: Version, Providers: []updater.Provider{provider}, PublicKey: tc.key, Window: updater.WindowNone}); err != nil {
				t.Fatal(err)
			}
			manager.check, manager.download = checker.Check, checker.DownloadAndInstall
			if _, err := app.CheckAppUpdate(); err != nil {
				t.Fatal(err)
			}
			if err := app.DownloadAppUpdate(); err != nil {
				t.Fatal(err)
			}
			waitAppUpdateState(t, manager, func(s AppUpdateStatus) bool { return s.State == "error" || s.State == "ready" })
			status := manager.status()
			if tc.fail && (status.State != "error" || status.ErrorStage != "verify" || !status.UpdateAvailable || status.LatestVersion != "99.0.0") {
				t.Fatalf("校验未拒绝或丢失状态: %+v", status)
			}
			if !tc.fail && status.State != "ready" {
				t.Fatalf("合法签名未通过: %+v", status)
			}
		})
	}
	if requests.Load() != 2 {
		t.Fatalf("安装包下载次数=%d", requests.Load())
	}
}

func waitAppUpdateState(t *testing.T, m *appUpdateManager, condition func(AppUpdateStatus) bool) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if condition(m.status()) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("等待状态超时: %+v", m.status())
		case <-ticker.C:
		}
	}
}
