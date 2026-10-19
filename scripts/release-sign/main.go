// release-sign 为发布安装包生成 sha256 摘要的 ed25519 签名。
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(os.Args[1:], os.Getenv("SINUO_UPDATE_SIGNING_KEY")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, key string) error {
	if len(args) == 2 && args[0] == "keygen" {
		return keygen(args[1])
	}
	if len(args) >= 2 && args[0] == "sign" {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(key))
		if err != nil || len(raw) != ed25519.PrivateKeySize {
			return errors.New("SINUO_UPDATE_SIGNING_KEY 缺失或不是有效的 ed25519 私钥")
		}
		// 拒绝种子与缓存公钥不一致的 64 字节私钥。
		private := ed25519.NewKeyFromSeed(raw[:ed25519.SeedSize])
		if !private.Equal(ed25519.PrivateKey(raw)) {
			return errors.New("SINUO_UPDATE_SIGNING_KEY 不是有效的 ed25519 私钥")
		}
		for _, path := range args[1:] {
			digest, err := fileDigest(path)
			if err != nil {
				return err
			}
			signature := base64.StdEncoding.EncodeToString(ed25519.Sign(private, digest)) + "\n"
			if err := os.WriteFile(path+".sig", []byte(signature), 0644); err != nil {
				return fmt.Errorf("写入签名: %w", err)
			}
		}
		return nil
	}
	if len(args) == 3 && args[0] == "verify" {
		public, err := base64.StdEncoding.DecodeString(strings.TrimSpace(args[1]))
		if err != nil || len(public) != ed25519.PublicKeySize {
			return errors.New("无效的 ed25519 公钥")
		}
		digest, err := fileDigest(args[2])
		if err != nil {
			return err
		}
		sidecar, err := os.ReadFile(args[2] + ".sig")
		if err != nil {
			return fmt.Errorf("读取签名: %w", err)
		}
		signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sidecar)))
		if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(public, digest, signature) {
			return errors.New("签名校验失败")
		}
		return nil
	}
	return errors.New("用法: release-sign keygen <目录> | sign <文件>... | verify <公钥 base64> <文件>")
}

func keygen(dir string) error {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("生成密钥: %w", err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// O_EXCL 防止覆盖已经保管的密钥。
	for _, item := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{{"private.key", private, 0600}, {"public.key", public, 0644}} {
		file, err := os.OpenFile(filepath.Join(dir, item.name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, item.mode)
		if err != nil {
			return fmt.Errorf("创建密钥文件: %w", err)
		}
		_, writeErr := io.WriteString(file, base64.StdEncoding.EncodeToString(item.data)+"\n")
		if err := errors.Join(writeErr, file.Close()); err != nil {
			return fmt.Errorf("写入密钥文件: %w", err)
		}
	}
	return nil
}

func fileDigest(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读取待签名文件: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return nil, fmt.Errorf("计算摘要: %w", err)
	}
	return hash.Sum(nil), nil
}
