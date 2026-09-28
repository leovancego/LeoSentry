package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

//go:embed static
var static embed.FS

type asset struct {
	data, gz    []byte
	contentType string
	etag        string
}

// Handler 返回内嵌静态页面的处理器。所有文件在创建时读入内存，
// 文本类文件预先 gzip 压缩一次，之后每次请求直接写出，不再占用路由器 CPU。
// 文件随二进制版本变化，用内容哈希作 ETag，浏览器每次校验、未变化时返回 304。
func Handler() (http.Handler, error) {
	root, err := fs.Sub(static, "static")
	if err != nil {
		return nil, err
	}
	assets := make(map[string]*asset)
	err = fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return err
		}
		data, err := fs.ReadFile(root, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		a := &asset{
			data:        data,
			contentType: mime.TypeByExtension(path.Ext(p)),
			etag:        `"` + hex.EncodeToString(sum[:8]) + `"`,
		}
		if compressible(a.contentType) {
			var buf bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			zw.Write(data)
			zw.Close()
			if buf.Len() < len(data) {
				a.gz = buf.Bytes()
			}
		}
		assets["/"+p] = a
		return nil
	})
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/" {
			p = "/index.html"
		}
		a, ok := assets[p]
		if !ok {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", a.contentType)
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", a.etag)
		h.Set("X-Content-Type-Options", "nosniff")
		if a.gz != nil {
			h.Set("Vary", "Accept-Encoding")
		}
		if r.Header.Get("If-None-Match") == a.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		body := a.data
		if a.gz != nil && AcceptsGzip(r) {
			h.Set("Content-Encoding", "gzip")
			body = a.gz
		}
		if r.Method != http.MethodHead {
			w.Write(body)
		}
	}), nil
}

func compressible(contentType string) bool {
	for _, t := range []string{"text/", "application/javascript", "application/json", "image/svg+xml"} {
		if strings.HasPrefix(contentType, t) {
			return true
		}
	}
	return false
}

// AcceptsGzip 判断客户端是否接受 gzip 编码的响应。
func AcceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}
