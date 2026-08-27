package evalcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

var githubCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

var githubAPIHosts = []HostRule{{Host: "api.github.com"}}
var githubRawHosts = []HostRule{{Host: "raw.githubusercontent.com"}}

func resolveGitHubCommit(
	ctx context.Context,
	downloader Downloader,
	directory string,
	repository string,
	ref string,
	filename string,
) (string, error) {
	download, err := downloader.Download(ctx, directory, DownloadRequest{
		URL:      "https://api.github.com/repos/" + repository + "/commits/" + ref,
		Filename: filename, AllowedHosts: githubAPIHosts, MaxBytes: 2 << 20,
	})
	if err != nil {
		return "", err
	}
	contents, err := os.ReadFile(download.Path)
	if err != nil {
		return "", fmt.Errorf("read GitHub commit metadata: %w", err)
	}
	var metadata struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(contents, &metadata); err != nil {
		return "", fmt.Errorf("decode GitHub commit metadata: %w", err)
	}
	if !githubCommitPattern.MatchString(metadata.SHA) {
		return "", fmt.Errorf("GitHub repository %s returned an invalid commit", repository)
	}
	return metadata.SHA, nil
}

func resolveGitHubDefaultBranch(
	ctx context.Context,
	downloader Downloader,
	directory string,
	repository string,
	filename string,
) (string, error) {
	download, err := downloader.Download(ctx, directory, DownloadRequest{
		URL:      "https://api.github.com/repos/" + repository,
		Filename: filename, AllowedHosts: githubAPIHosts, MaxBytes: 2 << 20,
	})
	if err != nil {
		return "", err
	}
	contents, err := os.ReadFile(download.Path)
	if err != nil {
		return "", fmt.Errorf("read GitHub repository metadata: %w", err)
	}
	var metadata struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(contents, &metadata); err != nil {
		return "", fmt.Errorf("decode GitHub repository metadata: %w", err)
	}
	if metadata.DefaultBranch == "" {
		return "", fmt.Errorf("GitHub repository %s has no default branch", repository)
	}
	return metadata.DefaultBranch, nil
}
