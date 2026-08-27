package evalcatalog

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type HostRule struct {
	Host            string
	AllowSubdomains bool
}

type DownloadRequest struct {
	URL          string
	Filename     string
	AllowedHosts []HostRule
	MaxBytes     int64
}

type Download struct {
	Path   string
	URL    string
	SHA256 string
	Header http.Header
}

type Downloader interface {
	Download(context.Context, string, DownloadRequest) (Download, error)
}

type IPResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type RestrictedDownloader struct {
	resolver  IPResolver
	transport http.RoundTripper
}

func NewRestrictedDownloader(resolver IPResolver, transport http.RoundTripper) *RestrictedDownloader {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if transport == nil {
		transport = restrictedTransport(resolver)
	}
	return &RestrictedDownloader{resolver: resolver, transport: transport}
}

func (downloader *RestrictedDownloader) Download(ctx context.Context, directory string, request DownloadRequest) (Download, error) {
	if downloader == nil || downloader.resolver == nil || downloader.transport == nil {
		return Download{}, errors.New("restricted downloader is not configured")
	}
	if request.Filename == "" || filepath.Base(request.Filename) != request.Filename || request.Filename == "." {
		return Download{}, errors.New("download filename must be a plain filename")
	}
	if len(request.AllowedHosts) == 0 {
		return Download{}, errors.New("download requires allowed hosts")
	}
	maxBytes := request.MaxBytes
	if maxBytes == 0 {
		maxBytes = maxImportBytes
	}
	if maxBytes < 0 || maxBytes > maxImportBytes {
		return Download{}, fmt.Errorf("download size limit must be between 1 and %d bytes", maxImportBytes)
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Download{}, fmt.Errorf("parse download URL: %w", err)
	}
	if err := downloader.validateDestination(ctx, parsed, request.AllowedHosts); err != nil {
		return Download{}, err
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Download{}, fmt.Errorf("create download request: %w", err)
	}
	client := &http.Client{
		Transport: downloader.transport,
		CheckRedirect: func(next *http.Request, previous []*http.Request) error {
			if len(previous) >= 10 {
				return errors.New("too many download redirects")
			}
			return downloader.validateDestination(next.Context(), next.URL, request.AllowedHosts)
		},
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return Download{}, fmt.Errorf("download request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Download{}, fmt.Errorf("download returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxBytes {
		return Download{}, fmt.Errorf("download exceeds %d bytes", maxBytes)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return Download{}, fmt.Errorf("create download directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".download-*.tmp")
	if err != nil {
		return Download{}, fmt.Errorf("create download temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return Download{}, fmt.Errorf("secure download temporary file: %w", err)
	}
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return Download{}, fmt.Errorf("read download response: %w", err)
	}
	if written > maxBytes {
		return Download{}, fmt.Errorf("download exceeds %d bytes", maxBytes)
	}
	if err := temporary.Sync(); err != nil {
		return Download{}, fmt.Errorf("sync download: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return Download{}, fmt.Errorf("close download: %w", err)
	}
	finalPath := filepath.Join(directory, request.Filename)
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return Download{}, fmt.Errorf("install download: %w", err)
	}
	keep = true
	finalURL := parsed.String()
	if response.Request != nil && response.Request.URL != nil {
		finalURL = response.Request.URL.String()
	}
	return Download{
		Path: finalPath, URL: finalURL, SHA256: fmt.Sprintf("%x", digest.Sum(nil)),
		Header: response.Header.Clone(),
	}, nil
}

func (downloader *RestrictedDownloader) validateDestination(ctx context.Context, destination *url.URL, rules []HostRule) error {
	if destination == nil || destination.Scheme != "https" || destination.Host == "" {
		return errors.New("download URL must use HTTPS")
	}
	if destination.User != nil {
		return errors.New("download URL must not contain user info")
	}
	if destination.Port() != "" && destination.Port() != "443" {
		return errors.New("download URL must use port 443")
	}
	host := strings.ToLower(strings.TrimSuffix(destination.Hostname(), "."))
	if host == "" || net.ParseIP(host) != nil || !hostAllowed(host, rules) {
		return fmt.Errorf("download host %q is not allowed", host)
	}
	addresses, err := downloader.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve download host %q: %w", host, err)
	}
	if err := validatePublicAddresses(host, addresses); err != nil {
		return err
	}
	return nil
}

func hostAllowed(host string, rules []HostRule) bool {
	for _, rule := range rules {
		allowed := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(rule.Host), "."))
		if allowed == "" {
			continue
		}
		if host == allowed || (rule.AllowSubdomains && strings.HasSuffix(host, "."+allowed)) {
			return true
		}
	}
	return false
}

func validatePublicAddresses(host string, addresses []net.IPAddr) error {
	if len(addresses) == 0 {
		return fmt.Errorf("download host %q resolved to no addresses", host)
	}
	for _, address := range addresses {
		ip := address.IP
		if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
			ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("download host %q resolved to a non-public address", host)
		}
	}
	return nil
}

func restrictedTransport(resolver IPResolver) *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addresses, err := resolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			if err := validatePublicAddresses(host, addresses); err != nil {
				return nil, err
			}
			var lastErr error
			for _, resolved := range addresses {
				connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
				if dialErr == nil {
					return connection, nil
				}
				lastErr = dialErr
			}
			return nil, lastErr
		},
	}
}
