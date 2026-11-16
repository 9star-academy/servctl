package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type releases struct {
	repo   string
	client *http.Client
}

func newReleases() releases {
	return releases{
		repo: "https://github.com/9star-academy/servctl",
		client: &http.Client{
			Timeout: 3 * time.Minute,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if req.URL.Scheme != "https" || req.URL.User != nil || len(via) >= 10 {
					return fmt.Errorf("下载地址跳转无效")
				}
				return nil
			},
		},
	}
}

func (r releases) latest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	client := *r.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("查询最新正式版: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return "", fmt.Errorf("查询最新正式版失败（HTTP %d）", resp.StatusCode)
	}
	location, err := req.URL.Parse(resp.Header.Get("Location"))
	if err != nil || location.Scheme != req.URL.Scheme || location.Host != req.URL.Host || location.User != nil {
		return "", fmt.Errorf("无法解析最新正式版的跳转地址")
	}
	tagPath := strings.TrimSuffix(req.URL.Path, "/latest") + "/tag/"
	tag, found := strings.CutPrefix(location.Path, tagPath)
	if !found || tag == "" || strings.Contains(tag, "/") {
		return "", fmt.Errorf("未找到最新正式版的版本号")
	}
	return tag, nil
}

func (r releases) download(ctx context.Context, version, goarch string) ([]byte, error) {
	if goarch != "amd64" && goarch != "arm64" {
		return nil, fmt.Errorf("不支持的 CPU 架构: %s", goarch)
	}
	archive := "servctl_linux_" + goarch + ".tar.gz"
	base := r.repo + "/releases/download/" + url.PathEscape(version) + "/"
	manifest, err := r.readAsset(ctx, base+"checksums.txt", 64<<10)
	if err != nil {
		return nil, fmt.Errorf("下载校验文件: %w", err)
	}
	checksum, err := releaseChecksum(manifest, archive)
	if err != nil {
		return nil, err
	}
	data, err := r.readAsset(ctx, base+archive, 32<<20)
	if err != nil {
		return nil, fmt.Errorf("下载 %s: %w", archive, err)
	}
	if sha256.Sum256(data) != checksum {
		return nil, fmt.Errorf("%s 的 SHA-256 校验失败", archive)
	}
	return data, nil
}

func releaseChecksum(manifest []byte, archive string) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	var checksum string
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != archive {
			continue
		}
		if checksum != "" || len(fields) != 2 {
			return digest, fmt.Errorf("校验文件缺少 %s 的唯一有效 SHA-256 条目", archive)
		}
		checksum = fields[0]
	}
	if len(checksum) != sha256.Size*2 {
		return digest, fmt.Errorf("校验文件缺少 %s 的唯一有效 SHA-256 条目", archive)
	}
	if _, err := hex.Decode(digest[:], []byte(checksum)); err != nil {
		return digest, fmt.Errorf("%s 的 SHA-256 校验值无效", archive)
	}
	return digest, nil
}

func (r releases) readAsset(ctx context.Context, assetURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("文件超过允许的大小")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("文件超过允许的大小")
	}
	return data, nil
}
