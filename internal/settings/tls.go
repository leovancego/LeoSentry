package settings

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TLS 是设置页里的端口和 HTTPS 证书状态。
type TLS struct {
	CertFile     string `json:"tlsCert"`
	KeyFile      string `json:"tlsKey"`
	HTTPPort     int    `json:"httpPort"`
	HTTPSPort    int    `json:"httpsPort"`
	RunningHTTP  int    `json:"httpRunningPort"`
	RunningHTTPS int    `json:"httpsRunningPort"`
	RunningCert  string `json:"tlsRunningCert"`
	RunningKey   string `json:"tlsRunningKey"`
	Active       bool   `json:"tlsActive"`
	Error        string `json:"tlsError,omitempty"`
}

// UseTLS 记录本次启动采用的证书路径和端口。路径来自 UCI，并已被数据库里保存的值覆盖。
func (s *Service) UseTLS(cert, key string, httpPort, httpsPort int) {
	s.tlsCert = strings.TrimSpace(cert)
	s.tlsKey = strings.TrimSpace(key)
	s.httpPort = httpPort
	s.tlsPort = httpsPort
	s.runningHTTP = httpPort
	s.runningHTTPS = httpsPort
}

// NoteTLS 记录启动检查的结果。running 为空表示这次没有启用 HTTPS。
func (s *Service) NoteTLS(runningCert, runningKey, checkErr string) {
	s.tlsRunningCert = runningCert
	s.tlsRunningKey = runningKey
	s.tlsError = checkErr
}

func (s *Service) tlsView() TLS {
	return TLS{
		CertFile:     s.tlsCert,
		KeyFile:      s.tlsKey,
		HTTPPort:     s.httpPort,
		HTTPSPort:    s.tlsPort,
		RunningHTTP:  s.runningHTTP,
		RunningHTTPS: s.runningHTTPS,
		RunningCert:  s.tlsRunningCert,
		RunningKey:   s.tlsRunningKey,
		Active:       s.tlsError == "" && s.tlsRunningCert != "" && s.tlsRunningKey != "",
		Error:        s.tlsError,
	}
}

// SaveTLS 保存端口和证书路径。返回的 restart 表示和当前进程已经加载的值不同，要重新启动才生效。
func (s *Service) SaveTLS(ctx context.Context, cert, key string, httpPort, httpsPort int) (View, bool, error) {
	cert = strings.TrimSpace(cert)
	key = strings.TrimSpace(key)
	if err := CheckPorts(httpPort, httpsPort); err != nil {
		return View{}, false, &Error{Message: err.Error()}
	}
	if err := CheckTLS(cert, key); err != nil {
		return View{}, false, &Error{Message: err.Error()}
	}
	if err := s.db.SetSetting(ctx, keyHTTPPort, strconv.Itoa(httpPort)); err != nil {
		return View{}, false, err
	}
	if err := s.db.SetSetting(ctx, keyHTTPSPort, strconv.Itoa(httpsPort)); err != nil {
		return View{}, false, err
	}
	if err := s.db.SetSetting(ctx, keyTLSCert, cert); err != nil {
		return View{}, false, err
	}
	if err := s.db.SetSetting(ctx, keyTLSKey, key); err != nil {
		return View{}, false, err
	}
	s.httpPort, s.tlsPort = httpPort, httpsPort
	s.tlsCert, s.tlsKey = cert, key
	s.tlsError = ""
	view, err := s.View(ctx)
	if err != nil {
		return View{}, false, err
	}
	restart := cert != s.tlsRunningCert || key != s.tlsRunningKey || httpPort != s.runningHTTP || httpsPort != s.runningHTTPS
	return view, restart, nil
}

// CheckPorts 校验两个监听端口。0 表示关闭该协议，但不能两个都关，也不能用同一个端口。
func CheckPorts(httpPort, httpsPort int) error {
	if httpPort < 0 || httpPort > 65535 || httpsPort < 0 || httpsPort > 65535 {
		return errors.New("端口要是 0 到 65535 的整数")
	}
	if httpPort == 0 && httpsPort == 0 {
		return errors.New("HTTP 和 HTTPS 不能同时关闭")
	}
	if httpPort > 0 && httpPort == httpsPort {
		return errors.New("HTTP 和 HTTPS 端口不能相同")
	}
	return nil
}

var portPattern = regexp.MustCompile(`^[0-9]{1,5}$`)

// ParsePort 只接受十进制整数字符串，拒绝小数、空格和其它字符。
func ParsePort(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if !portPattern.MatchString(raw) {
		return 0, errors.New("端口要是 0 到 65535 的整数")
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n > 65535 {
		return 0, errors.New("端口要是 0 到 65535 的整数")
	}
	return n, nil
}

// CheckTLS 检查证书和私钥。两者都空表示关闭 HTTPS。启动和保存时用同一套规则。
func CheckTLS(certFile, keyFile string) error {
	if certFile == "" && keyFile == "" {
		return nil
	}
	if certFile == "" || keyFile == "" {
		return errors.New("证书和私钥要一起填写，或一起留空")
	}
	if len(certFile) > 1024 || len(keyFile) > 1024 {
		return errors.New("证书路径太长")
	}
	if !filepath.IsAbs(certFile) || !filepath.IsAbs(keyFile) {
		return errors.New("证书和私钥都要填绝对路径")
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("找不到证书或私钥文件")
		}
		if os.IsPermission(err) {
			return errors.New("没有权限读取证书或私钥")
		}
		return errors.New("证书和私钥不匹配，或文件内容不正确")
	}
	if len(pair.Certificate) == 0 {
		return errors.New("证书文件是空的")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return errors.New("证书内容无法识别")
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) {
		return errors.New("证书还没到生效时间")
	}
	if now.After(leaf.NotAfter) {
		return errors.New("证书已经过期")
	}
	return nil
}
