package evalcatalog

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestDownloaderRejectsUnsafeRequestsBeforeWriting(t *testing.T) {
	publicResolver := staticIPResolver{addresses: map[string][]net.IPAddr{
		"official.example": {{IP: net.ParseIP("203.0.113.10")}},
	}}
	downloader := NewRestrictedDownloader(publicResolver, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return downloadResponse(request, http.StatusOK, "ok"), nil
	}))
	rules := []HostRule{{Host: "official.example"}}
	for _, test := range []struct {
		name    string
		request DownloadRequest
	}{
		{name: "non https", request: DownloadRequest{URL: "http://official.example/data", Filename: "data.json", AllowedHosts: rules}},
		{name: "userinfo", request: DownloadRequest{URL: "https://user@official.example/data", Filename: "data.json", AllowedHosts: rules}},
		{name: "non standard port", request: DownloadRequest{URL: "https://official.example:8443/data", Filename: "data.json", AllowedHosts: rules}},
		{name: "unlisted host", request: DownloadRequest{URL: "https://official.example.evil.test/data", Filename: "data.json", AllowedHosts: rules}},
		{name: "path traversal", request: DownloadRequest{URL: "https://official.example/data", Filename: "../data.json", AllowedHosts: rules}},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			if _, err := downloader.Download(context.Background(), directory, test.request); err == nil {
				t.Fatal("Download accepted unsafe request")
			}
			assertDirectoryEmpty(t, directory)
		})
	}
}

func TestDownloaderRejectsPrivateResolvedAddresses(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.8", "169.254.10.2", "::1", "fc00::1"} {
		t.Run(address, func(t *testing.T) {
			downloader := NewRestrictedDownloader(staticIPResolver{addresses: map[string][]net.IPAddr{
				"official.example": {{IP: net.ParseIP(address)}},
			}}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return downloadResponse(request, http.StatusOK, "ok"), nil
			}))
			directory := t.TempDir()
			_, err := downloader.Download(context.Background(), directory, DownloadRequest{
				URL: "https://official.example/data", Filename: "data.json",
				AllowedHosts: []HostRule{{Host: "official.example"}},
			})
			if err == nil {
				t.Fatal("Download accepted a private resolved address")
			}
			assertDirectoryEmpty(t, directory)
		})
	}
}

func TestDownloaderRevalidatesRedirectHosts(t *testing.T) {
	resolver := staticIPResolver{addresses: map[string][]net.IPAddr{
		"official.example": {{IP: net.ParseIP("203.0.113.10")}},
		"evil.example":     {{IP: net.ParseIP("203.0.113.11")}},
	}}
	downloader := NewRestrictedDownloader(resolver, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		response := downloadResponse(request, http.StatusFound, "")
		response.Header.Set("Location", "https://evil.example/stolen")
		return response, nil
	}))
	directory := t.TempDir()
	_, err := downloader.Download(context.Background(), directory, DownloadRequest{
		URL: "https://official.example/data", Filename: "data.json",
		AllowedHosts: []HostRule{{Host: "official.example"}},
	})
	if err == nil {
		t.Fatal("Download followed a redirect to an unlisted host")
	}
	assertDirectoryEmpty(t, directory)
}

func TestDownloaderRejectsNonSuccessAndOversizedResponsesWithoutPartialFiles(t *testing.T) {
	resolver := staticIPResolver{addresses: map[string][]net.IPAddr{
		"official.example": {{IP: net.ParseIP("203.0.113.10")}},
	}}
	for _, test := range []struct {
		name      string
		transport http.RoundTripper
	}{
		{
			name: "non success",
			transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return downloadResponse(request, http.StatusBadGateway, "bad"), nil
			}),
		},
		{
			name: "size overflow",
			transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return downloadResponse(request, http.StatusOK, strings.Repeat("x", 9)), nil
			}),
		},
		{
			name: "partial read failure",
			transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				response := downloadResponse(request, http.StatusOK, "")
				response.Body = io.NopCloser(&failingReader{})
				response.ContentLength = -1
				return response, nil
			}),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			downloader := NewRestrictedDownloader(resolver, test.transport)
			directory := t.TempDir()
			_, err := downloader.Download(context.Background(), directory, DownloadRequest{
				URL: "https://official.example/data", Filename: "data.json", MaxBytes: 8,
				AllowedHosts: []HostRule{{Host: "official.example"}},
			})
			if err == nil {
				t.Fatal("Download accepted an invalid response")
			}
			assertDirectoryEmpty(t, directory)
		})
	}
}

func TestDownloaderAllowsExplicitSubdomainsAndReturnsDigest(t *testing.T) {
	resolver := staticIPResolver{addresses: map[string][]net.IPAddr{
		"cdn.hf.co": {{IP: net.ParseIP("203.0.113.10")}},
	}}
	downloader := NewRestrictedDownloader(resolver, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return downloadResponse(request, http.StatusOK, "payload"), nil
	}))
	directory := t.TempDir()
	download, err := downloader.Download(context.Background(), directory, DownloadRequest{
		URL: "https://cdn.hf.co/data", Filename: "data.json",
		AllowedHosts: []HostRule{{Host: "hf.co", AllowSubdomains: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if download.SHA256 != "239f59ed55e737c77147cf55ad0c1b030b6d7ee748a7426952f9b852d5a935e5" {
		t.Fatalf("SHA256=%q", download.SHA256)
	}
	contents, err := os.ReadFile(download.Path)
	if err != nil || string(contents) != "payload" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
}

type staticIPResolver struct {
	addresses map[string][]net.IPAddr
	err       error
}

func (resolver staticIPResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if resolver.err != nil {
		return nil, resolver.err
	}
	addresses, found := resolver.addresses[host]
	if !found {
		return nil, errors.New("host not found")
	}
	return addresses, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func downloadResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Status:        http.StatusText(status),
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader(body)),
		Request:       request,
		ContentLength: int64(len(body)),
	}
}

type failingReader struct{ read bool }

func (reader *failingReader) Read(buffer []byte) (int, error) {
	if reader.read {
		return 0, errors.New("read failed")
	}
	reader.read = true
	copy(buffer, "part")
	return 4, nil
}

func assertDirectoryEmpty(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial files remain: %+v", entries)
	}
}
