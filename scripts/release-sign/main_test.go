package main

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseSign_RoundTripAndTamper(t *testing.T) {
	// 只生成临时测试密钥，不调用人工 keygen 子命令，不触碰正式密钥。
	dir := t.TempDir()
	if err := keygen(dir); err != nil {
		t.Fatal(err)
	}
	private, err := os.ReadFile(filepath.Join(dir, "private.key"))
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(filepath.Join(dir, "public.key"))
	if err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"private.key": 64, "public.key": 32} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(decoded) != size {
			t.Fatalf("%s 长度错误", name)
		}
	}
	info, _ := os.Stat(filepath.Join(dir, "private.key"))
	if info.Mode().Perm() != 0600 {
		t.Fatalf("private mode=%v", info.Mode())
	}
	if err := keygen(dir); err == nil {
		t.Fatal("覆盖了已存在密钥")
	}
	path := filepath.Join(dir, "test.zip")
	if err := os.WriteFile(path, []byte("test artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"sign", path}, string(private)); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", string(public), path}, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("Test artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", string(public), path}, ""); err == nil {
		t.Fatal("篡改文件仍通过")
	}
}

func TestReleaseSign_InvalidPrivateKeyDoesNotLeak(t *testing.T) {
	for _, key := range []string{"", "private-test-secret-not-base64", base64.StdEncoding.EncodeToString(make([]byte, 64))} {
		err := run([]string{"sign", filepath.Join(t.TempDir(), "absent")}, key)
		if err == nil || (key != "" && strings.Contains(err.Error(), key)) {
			t.Fatal("错误未拒绝私钥或泄漏私钥")
		}
	}
}

func TestReleaseSign_MissingKeyExitsNonzero(t *testing.T) {
	if os.Getenv("SINUO_SIGN_TEST_CHILD") == "1" {
		os.Args = []string{"release-sign", "sign", "test.zip"}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestReleaseSign_MissingKeyExitsNonzero$")
	cmd.Env = append(os.Environ(), "SINUO_SIGN_TEST_CHILD=1", "SINUO_UPDATE_SIGNING_KEY=")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "SINUO_UPDATE_SIGNING_KEY 缺失") {
		t.Fatalf("缺失私钥未非零退出: %s %v", output, err)
	}
}
