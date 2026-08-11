//go:build mage
// +build mage

package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	// mage:import
	build "github.com/grafana/grafana-plugin-sdk-go/build"
)

// Default configures the default target.
var Default = build.BuildAll

const (
	pluginID = "emreatayy-oracle-datasource"
	distDir  = "dist"
)

// Archive packages dist/ into dist/<plugin-id>-<version>.zip and
// dist/<plugin-id>-<version>.tar.gz, each with a single top-level
// <plugin-id>/ directory — the layout the plugin-validator and manual
// installs expect (§20, docs/release-and-packaging.md §1.2).
func Archive() error {
	if _, err := os.Stat(distDir); err != nil {
		return fmt.Errorf("no %s directory — run `mage -v` first: %w", distDir, err)
	}
	version, err := pluginVersion()
	if err != nil {
		return err
	}
	base := fmt.Sprintf("%s-%s", pluginID, version)
	if err := makeZip(filepath.Join(distDir, base+".zip")); err != nil {
		return err
	}
	if err := makeTarball(filepath.Join(distDir, base+".tar.gz")); err != nil {
		return err
	}
	fmt.Printf("archive: %s.zip\n", filepath.Join(distDir, base))
	fmt.Printf("archive: %s.tar.gz\n", filepath.Join(distDir, base))
	return nil
}

// Validate runs @grafana/plugin-validator against the current release
// archive in dist/. It fails on errors (structural/security) and reports
// warnings, which pre-publish are the expected publishing-flow items (unsigned
// plugin, unregistered account, private-repo links, screenshots). Requires
// `mage archive` to have run first.
func Validate() error {
	version, err := pluginVersion()
	if err != nil {
		return err
	}
	zipPath := filepath.Join(distDir, fmt.Sprintf("%s-%s.zip", pluginID, version))
	if _, err := os.Stat(zipPath); err != nil {
		return fmt.Errorf("no archive %s — run `mage archive` first: %w", zipPath, err)
	}
	cmd := exec.Command("node_modules/.bin/plugin-validator", zipPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// pluginVersion reads info.version from dist/plugin.json — the build
// substitutes %VERSION% from package.json during compilation.
func pluginVersion() (string, error) {
	var meta struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
	}
	raw, err := os.ReadFile(filepath.Join(distDir, "plugin.json"))
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return "", err
	}
	if meta.Info.Version == "" {
		return "", fmt.Errorf("dist/plugin.json has an empty info.version")
	}
	return meta.Info.Version, nil
}

// archiveFileName reports whether p (a path under dist/) is one of our own
// release archives, so re-running archive() never packs a previous archive.
// Note filepath.Ext returns only the final extension — ".tar.gz" arrives as
// ".gz" — so ".tar" is covered for safety as well.
func archiveFileName(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".zip", ".tar", ".gz":
		return true
	}
	return false
}

func makeZip(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := zip.NewWriter(f)
	defer w.Close()
	return filepath.Walk(distDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p == distDir || archiveFileName(p) {
			return nil
		}
		rel, err := filepath.Rel(distDir, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Join(pluginID, rel))
		if info.IsDir() {
			_, err := w.Create(name + "/")
			return err
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(info.Mode())
		entry, err := w.CreateHeader(header)
		if err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(entry, src)
		return err
	})
}

func makeTarball(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	return filepath.Walk(distDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p == distDir || archiveFileName(p) {
			return nil
		}
		rel, err := filepath.Rel(distDir, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Join(pluginID, rel))
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = name
		if info.IsDir() {
			header.Name += "/"
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(tw, src)
		return err
	})
}
