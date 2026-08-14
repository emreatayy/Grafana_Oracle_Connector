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
	"io/fs"
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

// PlatformArch holds OS/Arch pairs for cross-compilation.
type PlatformArch struct {
	GOOS   string
	GOARCH string
	Ext    string // executable extension
	Suffix string // filename suffix e.g. "linux_amd64"
}

var platforms = []PlatformArch{
	{GOOS: "linux", GOARCH: "amd64", Ext: "", Suffix: "linux_amd64"},
	{GOOS: "linux", GOARCH: "arm64", Ext: "", Suffix: "linux_arm64"},
	{GOOS: "linux", GOARCH: "arm", Ext: "", Suffix: "linux_arm"},
	{GOOS: "darwin", GOARCH: "amd64", Ext: "", Suffix: "darwin_amd64"},
	{GOOS: "darwin", GOARCH: "arm64", Ext: "", Suffix: "darwin_arm64"},
	{GOOS: "windows", GOARCH: "amd64", Ext: ".exe", Suffix: "windows_amd64"},
}

// binaryName returns the built binary filename for a platform.
func binaryName(p PlatformArch) string {
	return fmt.Sprintf("gpx_oracle_datasource_%s%s", p.Suffix, p.Ext)
}

// packageFileName returns the archive filename for a platform package.
func packageFileName(version string, p PlatformArch) string {
	return fmt.Sprintf("%s-%s-%s", pluginID, version, p.Suffix)
}

// buildPlatform builds the backend for a specific platform if not already built.
func buildPlatform(p PlatformArch) error {
	binary := filepath.Join(distDir, binaryName(p))
	if _, err := os.Stat(binary); err == nil {
		fmt.Printf("Binary already exists: %s\n", binary)
		return nil
	}
	fmt.Printf("Building for %s/%s...\n", p.GOOS, p.GOARCH)
	cmd := exec.Command("go", "build", "-o", binary, "-ldflags", "-s -w", "./pkg/plugin")
	cmd.Env = append(os.Environ(), "GOOS="+p.GOOS, "GOARCH="+p.GOARCH, "CGO_ENABLED=0")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// copyPluginFiles copies plugin.json, module.js, img/, LICENSE, README.md, CHANGELOG.md
// to a temporary staging directory for packaging.
func copyPluginFiles(stageDir string) error {
	files := []string{
		"plugin.json",
		"module.js",
		"module.js.map",
		"LICENSE",
		"README.md",
		"CHANGELOG.md",
	}
	for _, f := range files {
		src := filepath.Join(distDir, f)
		dst := filepath.Join(stageDir, f)
		if _, err := os.Stat(src); err == nil {
			if err := copyFile(src, dst); err != nil {
				return err
			}
		}
	}
	// Copy img/ directory
	imgSrc := filepath.Join(distDir, "img")
	imgDst := filepath.Join(stageDir, "img")
	if _, err := os.Stat(imgSrc); err == nil {
		if err := copyDir(imgSrc, imgDst); err != nil {
			return err
		}
	}
	return nil
}

// copyFile copies a single file.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// copyDir recursively copies a directory.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		return copyFile(path, target)
	})
}

// packagePlatform creates a tar.gz and zip for a specific platform.
// Each package contains: plugin.json, module.js, module.js.map, LICENSE, README.md, CHANGELOG.md, img/, and the platform-specific binary renamed to gpx_oracle_datasource.
func packagePlatform(p PlatformArch) error {
	version, err := pluginVersion()
	if err != nil {
		return err
	}
	if err := buildPlatform(p); err != nil {
		return err
	}

	base := packageFileName(version, p)
	tmpDir := filepath.Join(distDir, "tmp_"+base)
	pluginDir := filepath.Join(tmpDir, pluginID)

	// Clean up any previous tmp dir
	os.RemoveAll(tmpDir)
	defer os.RemoveAll(tmpDir)

	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		return err
	}

	// Copy plugin files
	if err := copyPluginFiles(pluginDir); err != nil {
		return err
	}

	// Copy and rename the platform binary to gpx_oracle_datasource (or gpx_oracle_datasource.exe for Windows)
	binarySrc := filepath.Join(distDir, binaryName(p))
	binaryDst := filepath.Join(pluginDir, "gpx_oracle_datasource"+p.Ext)
	if err := copyFile(binarySrc, binaryDst); err != nil {
		return err
	}
	// Make binary executable
	if err := os.Chmod(binaryDst, 0755); err != nil {
		return err
	}

	// Create tar.gz
	tarPath := filepath.Join(distDir, base+".tar.gz")
	if err := makeTarballFromDir(tarPath, tmpDir, pluginID); err != nil {
		return err
	}
	fmt.Printf("Created: %s\n", tarPath)

	// Create zip
	zipPath := filepath.Join(distDir, base+".zip")
	if err := makeZipFromDir(zipPath, tmpDir, pluginID); err != nil {
		return err
	}
	fmt.Printf("Created: %s\n", zipPath)

	return nil
}

// makeTarballFromDir creates a tar.gz from a specific directory.
func makeTarballFromDir(outPath, srcDir, topLevel string) error {
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	return filepath.Walk(srcDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p == srcDir {
			return nil
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Join(topLevel, rel))
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

// makeZipFromDir creates a zip from a specific directory.
func makeZipFromDir(outPath, srcDir, topLevel string) error {
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	w := zip.NewWriter(f)
	defer w.Close()
	return filepath.Walk(srcDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p == srcDir {
			return nil
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Join(topLevel, rel))
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

// PackagePlatforms builds and packages the plugin for all supported platforms.
// Produces: dist/emreatayy-oracle-datasource-<version>-<platform>.tar.gz and .zip for each platform.
func PackagePlatforms() error {
	fmt.Println("Packaging for all platforms...")
	for _, p := range platforms {
		if err := packagePlatform(p); err != nil {
			return fmt.Errorf("failed to package %s: %w", p.Suffix, err)
		}
	}
	return nil
}

// SourceArchive creates source code archives (zip and tar.gz) excluding build artifacts, node_modules, .git, dist, verification, etc.
func SourceArchive() error {
	version, err := pluginVersion()
	if err != nil {
		return err
	}
	base := fmt.Sprintf("%s-%s-source", pluginID, version)
	zipPath := filepath.Join(distDir, base+".zip")
	tarPath := filepath.Join(distDir, base+".tar.gz")

	// Files/dirs to exclude
	exclude := map[string]bool{
		"dist": true, "node_modules": true, ".git": true, ".github": true,
		"verification": true, "devenv": true, "tests": true, "playwright": true,
		"test-results": true, "playwright-report": true, "examples": true,
		"pkg": true, "src": true, // source is in root, but we want to include them
	}

	// We'll walk the project root and include everything except excluded dirs
	projectRoot := "."
	return filepath.WalkDir(projectRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		// Check if any path component is excluded
		rel, _ := filepath.Rel(projectRoot, path)
		parts := strings.Split(rel, string(filepath.Separator))
		for _, part := range parts {
			if exclude[part] {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		// Skip hidden files/dirs except .config, .eslintrc etc
		if strings.HasPrefix(d.Name(), ".") && !strings.HasPrefix(d.Name(), ".config") && !strings.HasPrefix(d.Name(), ".eslint") && !strings.HasPrefix(d.Name(), ".prettier") && !strings.HasPrefix(d.Name(), ".golangci") && !strings.HasPrefix(d.Name(), ".npmrc") && !strings.HasPrefix(d.Name(), ".nvmrc") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		return nil
	})

	// Actually create archives using the same pattern as Archive() but from project root
	return createSourceArchives(zipPath, tarPath, base)
}

func createSourceArchives(zipPath, tarPath, topLevel string) error {
	// Create zip
	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer f.Close()
	w := zip.NewWriter(f)
	defer w.Close()

	exclude := map[string]bool{
		"dist": true, "node_modules": true, ".git": true, ".github": true,
		"verification": true, "devenv": true, "tests": true, "playwright": true,
		"test-results": true, "playwright-report": true, "examples": true,
	}

	if err := filepath.Walk(".", func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		rel, _ := filepath.Rel(".", p)
		parts := strings.Split(rel, string(filepath.Separator))
		for _, part := range parts {
			if exclude[part] {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		// Skip hidden files except important config files
		if strings.HasPrefix(info.Name(), ".") {
			keep := []string{".config", ".eslintrc", ".prettierrc", ".golangci.yml", ".npmrc", ".nvmrc", ".cprc.json"}
			keepIt := false
			for _, k := range keep {
				if strings.HasPrefix(info.Name(), k) {
					keepIt = true
					break
				}
			}
			if !keepIt {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		name := filepath.ToSlash(filepath.Join(topLevel, rel))
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
	}); err != nil {
		return err
	}

	// Create tar.gz
	tf, err := os.Create(tarPath)
	if err != nil {
		return err
	}
	defer tf.Close()
	gz := gzip.NewWriter(tf)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	return filepath.Walk(".", func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		rel, _ := filepath.Rel(".", p)
		parts := strings.Split(rel, string(filepath.Separator))
		for _, part := range parts {
			if exclude[part] {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if strings.HasPrefix(info.Name(), ".") {
			keep := []string{".config", ".eslintrc", ".prettierrc", ".golangci.yml", ".npmrc", ".nvmrc", ".cprc.json"}
			keepIt := false
			for _, k := range keep {
				if strings.HasPrefix(info.Name(), k) {
					keepIt = true
					break
				}
			}
			if !keepIt {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		name := filepath.ToSlash(filepath.Join(topLevel, rel))
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

// ReleaseAll runs the full release pipeline: buildAll → archive → packagePlatforms → sourceArchive.
func ReleaseAll() error {
	// build.BuildAll is a mage target, call via mage directly in workflow
	// Here we just run the packaging steps assuming build is done
	if err := Archive(); err != nil {
		return fmt.Errorf("archive failed: %w", err)
	}
	if err := PackagePlatforms(); err != nil {
		return fmt.Errorf("packagePlatforms failed: %w", err)
	}
	if err := SourceArchive(); err != nil {
		return fmt.Errorf("sourceArchive failed: %w", err)
	}
	fmt.Println("Release artifacts created in dist/")
	return nil
}
