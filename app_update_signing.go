package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/updater"
)

// 正式发布签名公钥；私钥只保存在 GitHub Secret SINUO_UPDATE_SIGNING_KEY 与维护者的离线备份中。
// 更换密钥会使已安装版本无法校验之后的更新，须先发布一个携带新公钥、仍由旧私钥签名的过渡版本。
var appUpdatePublicKey = "UdJdmnIbI/PpATa4oMtF9e7goPX0C3E93MwhbfNPoL4="

// 仅供隔离真机验收通过 ldflags 覆盖；空值使用 api.github.com。
var appUpdateAPIBase = ""

func decodeAppUpdatePublicKey(value string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("无效的软件更新公钥")
	}
	return key, nil
}

type signedGitHubProvider struct {
	inner      updater.Provider
	client     *http.Client
	logger     *slog.Logger
	onProgress func(int64, int64)
	onComplete func()
}

func (p *signedGitHubProvider) Name() string { return "github" }

func (p *signedGitHubProvider) Check(ctx context.Context, req updater.CheckRequest) (*updater.Release, error) {
	release, err := p.inner.Check(ctx, req)
	if err != nil || release == nil {
		return release, err
	}
	if release.Metadata == nil {
		release.Metadata = make(map[string]any)
	}
	// 不信任上游自行填写的签名字段，只有本次 sidecar 读取成功才标记已签名。
	release.Metadata["sinuo.signed"] = false
	if release.Verification != nil {
		release.Verification.Signature, release.Verification.SignatureAlgo = nil, ""
	}
	signature, signatureErr := p.signature(ctx, release)
	if signatureErr != nil {
		release.Metadata["sinuo.signature_error"] = signatureErr.Error()
		if p.logger != nil {
			p.logger.Warn("软件更新签名不可用", "version", release.Version, "error", signatureErr)
		}
		return release, nil
	}
	if release.Verification == nil {
		release.Verification = &updater.Verification{}
	}
	release.Verification.SignatureAlgo = "ed25519"
	release.Verification.Signature = signature
	release.Metadata["sinuo.signed"] = true
	delete(release.Metadata, "sinuo.signature_error")
	return release, nil
}

func (p *signedGitHubProvider) signature(ctx context.Context, release *updater.Release) ([]byte, error) {
	assetURL, ok := release.Metadata["github.asset.url"].(string)
	if !ok || assetURL == "" {
		return nil, errors.New("安装包地址缺失")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL+".sig", nil)
	if err != nil {
		return nil, errors.New("签名地址无效")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, errors.New("签名下载失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("签名 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1025))
	if err != nil || len(body) > 1024 {
		return nil, errors.New("签名读取失败或超过 1 KB")
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(body)))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, errors.New("签名编码或长度无效")
	}
	return signature, nil
}

func (p *signedGitHubProvider) Download(ctx context.Context, release *updater.Release, dst io.Writer, progress func(int64, int64)) error {
	if !appUpdateVerifiedRelease(release) {
		return errors.New("安装包缺少签名或校验值，已拒绝安装")
	}
	err := p.inner.Download(ctx, release, dst, func(written, total int64) {
		if progress != nil {
			progress(written, total)
		}
		if p.onProgress != nil {
			p.onProgress(written, total)
		}
	})
	if err == nil && p.onComplete != nil {
		p.onComplete()
	}
	return err
}

func appUpdateVerifiedRelease(release *updater.Release) bool {
	if release == nil || release.Verification == nil {
		return false
	}
	v := release.Verification
	return v.DigestAlgo == "sha256" && len(v.Digest) == sha256.Size &&
		v.SignatureAlgo == "ed25519" && len(v.Signature) == ed25519.SignatureSize
}
