package architecture

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type listedPackage struct {
	ImportPath string
	Imports    []string
	Module     *struct{ Path string }
}

func TestArchitecture(t *testing.T) {
	pkgs := loadPackages(t)
	module := modulePath(t, pkgs)

	internal := module + "/internal/"
	service := internal + "service"
	handler := internal + "handler"
	repository := internal + "repository"
	platform := internal + "platform"
	config := internal + "config"

	t.Run("services are independent", func(t *testing.T) {
		for _, pkg := range pkgs {
			if !strings.HasPrefix(pkg.ImportPath, service+"/") {
				continue
			}
			for _, imported := range pkg.Imports {
				if strings.HasPrefix(imported, service+"/") && imported != pkg.ImportPath {
					t.Errorf("%s imports %s: service packages must not depend on other service packages", pkg.ImportPath, imported)
				}
			}
		}
	})

	t.Run("handlers do not import the repository", func(t *testing.T) {
		for _, pkg := range pkgs {
			if pkg.ImportPath != handler && !strings.HasPrefix(pkg.ImportPath, handler+"/") {
				continue
			}
			for _, imported := range pkg.Imports {
				if imported == repository || strings.HasPrefix(imported, repository+"/") {
					t.Errorf("%s imports %s: handlers call services and must not bypass them for repository access", pkg.ImportPath, imported)
				}
			}
		}
	})

	t.Run("platform stays generic", func(t *testing.T) {
		for _, pkg := range pkgs {
			if !strings.HasPrefix(pkg.ImportPath, platform+"/") {
				continue
			}
			for _, imported := range pkg.Imports {
				if strings.HasPrefix(imported, internal) && !strings.HasPrefix(imported, platform+"/") {
					t.Errorf("%s imports %s: platform packages must not depend on application packages", pkg.ImportPath, imported)
				}
			}
		}
	})

	t.Run("repository does not import upward", func(t *testing.T) {
		for _, pkg := range pkgs {
			if pkg.ImportPath != repository && !strings.HasPrefix(pkg.ImportPath, repository+"/") {
				continue
			}
			for _, imported := range pkg.Imports {
				if imported == handler || strings.HasPrefix(imported, handler+"/") || imported == service || strings.HasPrefix(imported, service+"/") {
					t.Errorf("%s imports %s: repository packages must not depend on handlers or services", pkg.ImportPath, imported)
				}
			}
		}
	})

	t.Run("only composition imports config", func(t *testing.T) {
		for _, pkg := range pkgs {
			for _, imported := range pkg.Imports {
				if imported != config || allowedConfigImporter(pkg.ImportPath, module) {
					continue
				}
				t.Errorf("%s imports %s: only cmd, app, server, and config packages may read root configuration", pkg.ImportPath, imported)
			}
		}
	})
}

func allowedConfigImporter(importer, module string) bool {
	for _, prefix := range []string{
		module + "/cmd",
		module + "/internal/app",
		module + "/internal/server",
		module + "/internal/config",
	} {
		if importer == prefix || strings.HasPrefix(importer, prefix+"/") {
			return true
		}
	}
	return false
}

func loadPackages(t *testing.T) []listedPackage {
	t.Helper()
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = moduleRoot(t)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -json ./...: %v\n%s", err, output)
	}

	decoder := json.NewDecoder(strings.NewReader(string(output)))
	var pkgs []listedPackage
	for {
		var pkg listedPackage
		err := decoder.Decode(&pkg)
		if err == io.EOF {
			return pkgs
		}
		if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		pkgs = append(pkgs, pkg)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for dir := root; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find go.mod walking up from %s", root)
		}
	}
}

func modulePath(t *testing.T, pkgs []listedPackage) string {
	t.Helper()
	for _, pkg := range pkgs {
		if pkg.Module != nil && pkg.Module.Path != "" {
			return pkg.Module.Path
		}
	}
	t.Fatal("go list output did not include a module path")
	return ""
}
