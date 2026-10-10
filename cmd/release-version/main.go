package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"uniclog.io/sonoryx/internal/appversion"
)

func main() {
	tag := flag.String("tag", "", "optional release tag to validate against clientVersion")
	wailsConfig := flag.String("wails-config", "", "optional Wails YAML template to render")
	out := flag.String("out", "build/config.generated.yml", "rendered Wails config destination")
	flag.Parse()
	if err := run(*tag, *wailsConfig, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(appversion.ClientVersion)
}

func run(tag, wailsConfig, out string) error {
	if tag != "" && tag != "v"+appversion.ClientVersion {
		return fmt.Errorf("release tag %q does not match version/release.json clientVersion (%s)", tag, appversion.ClientVersion)
	}
	if wailsConfig == "" {
		return nil
	}
	sourcePath, err := filepath.Abs(wailsConfig)
	if err != nil {
		return err
	}
	outputPath, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	if sourcePath == outputPath || sameFile(sourcePath, outputPath) {
		return fmt.Errorf("generated Wails config must not overwrite its template")
	}
	config, err := os.ReadFile(wailsConfig)
	if err != nil {
		return fmt.Errorf("read Wails config: %w", err)
	}
	tmpl, err := template.New("wails-config").Option("missingkey=error").Parse(string(config))
	if err != nil {
		return fmt.Errorf("parse Wails config template: %w", err)
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, struct{ ClientVersion string }{appversion.ClientVersion}); err != nil {
		return fmt.Errorf("render Wails config: %w", err)
	}
	previous, err := os.ReadFile(out)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read generated Wails config: %w", err)
	}
	if bytes.Equal(previous, rendered.Bytes()) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return fmt.Errorf("create generated config directory: %w", err)
	}
	if err := os.WriteFile(out, rendered.Bytes(), 0644); err != nil {
		return fmt.Errorf("write generated Wails config: %w", err)
	}
	return nil
}

func sameFile(source, destination string) bool {
	sourceInfo, sourceErr := os.Stat(source)
	destinationInfo, destinationErr := os.Stat(destination)
	return sourceErr == nil && destinationErr == nil && os.SameFile(sourceInfo, destinationInfo)
}
