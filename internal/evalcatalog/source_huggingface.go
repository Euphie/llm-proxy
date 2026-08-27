package evalcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

var huggingFaceRevisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

var huggingFaceHosts = []HostRule{
	{Host: "huggingface.co", AllowSubdomains: true},
	{Host: "hf.co", AllowSubdomains: true},
}

func resolveHuggingFaceDatasetRevision(
	ctx context.Context,
	downloader Downloader,
	directory string,
	dataset string,
	filename string,
) (string, error) {
	download, err := downloader.Download(ctx, directory, DownloadRequest{
		URL: "https://huggingface.co/api/datasets/" + dataset, Filename: filename,
		AllowedHosts: huggingFaceHosts, MaxBytes: 2 << 20,
	})
	if err != nil {
		return "", err
	}
	contents, err := os.ReadFile(download.Path)
	if err != nil {
		return "", fmt.Errorf("read Hugging Face dataset metadata: %w", err)
	}
	var metadata struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(contents, &metadata); err != nil {
		return "", fmt.Errorf("decode Hugging Face dataset metadata: %w", err)
	}
	if !huggingFaceRevisionPattern.MatchString(metadata.SHA) {
		return "", fmt.Errorf("hugging Face dataset %s returned an invalid revision", dataset)
	}
	return metadata.SHA, nil
}
