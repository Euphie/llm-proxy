package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Euphie/llm-proxy/internal/evalcatalog"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string, stdout io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("evalcatalog-import", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "path to the pinned import manifest")
	outputPath := flags.String("output", "", "output path, or - for stdout")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *manifestPath == "" || *outputPath == "" {
		return fmt.Errorf("both -manifest and -output are required")
	}
	manifestFile, err := os.Open(*manifestPath)
	if err != nil {
		return fmt.Errorf("open evaluation import manifest: %w", err)
	}
	manifest, err := evalcatalog.DecodeImportManifest(manifestFile)
	closeErr := manifestFile.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return fmt.Errorf("close evaluation import manifest: %w", closeErr)
	}
	catalog, report, err := evalcatalog.BuildCatalog(manifest, filepath.Dir(*manifestPath))
	if err != nil {
		return err
	}
	if *outputPath == "-" {
		if err := evalcatalog.EncodeCatalog(stdout, catalog); err != nil {
			return fmt.Errorf("write evaluation catalog: %w", err)
		}
	} else if err := evalcatalog.WriteCatalog(*outputPath, catalog); err != nil {
		return fmt.Errorf("write evaluation catalog: %w", err)
	}
	fmt.Fprintf(stderr, "imported=%d skipped_unmapped=%d revision=%s\n", report.Imported, report.SkippedUnmapped, catalog.Revision)
	return nil
}
