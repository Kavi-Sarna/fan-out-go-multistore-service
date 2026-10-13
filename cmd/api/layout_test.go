package main

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The layering rules, as a test rather than as a paragraph in a README.
//
// This lives beside the composition root because that is what the rules are
// about: cmd/api is the one package allowed to name a concrete store, and
// everything below it works against an interface. An invariant nobody checks
// is a preference, and preferences lose arguments at 3am.

const module = "github.com/kavi-sarna/fan-out-go-multistore-service"

// Packages that are allowed to import a third-party driver, because wrapping
// one is their entire job. Paths are relative to the repository root.
var mayImportDrivers = []string{
	"internal/store/",
	"internal/kafka/",
	"internal/obs/",
}

// Packages nothing may import except cmd/api. These do not exist yet; the
// list is here so that adding one does not also require remembering to add
// a rule about it.
var implementations = []string{
	module + "/internal/store/pg",
	module + "/internal/store/cql",
	module + "/internal/kafka",
}

// imports returns every direct import in the Go files under dir, keyed by
// the package directory that contains them.
func imports(t *testing.T, root string) map[string][]string {
	t.Helper()

	found := map[string][]string{}
	fileSet := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		dir := filepath.ToSlash(filepath.Dir(path))
		for _, spec := range file.Imports {
			found[dir] = append(found[dir], strings.Trim(spec.Path.Value, `"`))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return found
}

// thirdParty reports whether an import path is outside the standard library
// and outside this module. The standard library has no dot in its first path
// element, which is the rule the go command itself uses.
func thirdParty(path string) bool {
	if strings.HasPrefix(path, module) {
		return false
	}
	first, _, _ := strings.Cut(path, "/")
	return strings.Contains(first, ".")
}

// A driver imported anywhere but its own adapter means the abstraction has
// already leaked, whatever the interfaces say.
func TestOnlyAdaptersImportThirdPartyPackages(t *testing.T) {
	t.Parallel()

	for dir, paths := range imports(t, "../../internal") {
		relative := strings.TrimPrefix(filepath.ToSlash(dir), "../../")
		allowed := false
		for _, prefix := range mayImportDrivers {
			if strings.HasPrefix(relative+"/", prefix) {
				allowed = true
				break
			}
		}
		if allowed {
			continue
		}
		for _, path := range paths {
			if thirdParty(path) {
				t.Errorf("%s imports %s; only %v may reach outside the standard library",
					relative, path, mayImportDrivers)
			}
		}
	}
}

// Only the composition root may name a concrete store. Everything else takes
// an interface, which is what makes the handlers testable without a database
// and what makes swapping an implementation a one-line change in main.
func TestOnlyTheCompositionRootNamesAnImplementation(t *testing.T) {
	t.Parallel()

	// Until one of these exists, the only way to trip this test is to import
	// a package that is not there — which fails the build for a reason that
	// has nothing to do with layering. Announced rather than passing quietly,
	// so the line in the output is honest about what is being checked.
	exists := false
	for _, implementation := range implementations {
		if _, err := os.Stat(filepath.Join("../..", strings.TrimPrefix(implementation, module+"/"))); err == nil {
			exists = true
			break
		}
	}
	if !exists {
		t.Skip("no implementation package exists yet; this begins enforcing when one does")
	}

	for dir, paths := range imports(t, "../../internal") {
		relative := strings.TrimPrefix(filepath.ToSlash(dir), "../../")
		for _, path := range paths {
			for _, implementation := range implementations {
				// A package importing itself is not a violation. Compared
				// directly rather than by suffix, which happened to work for
				// these three names and would not for the next one.
				if path == implementation && relative != strings.TrimPrefix(implementation, module+"/") {
					t.Errorf("%s imports %s; only cmd/api may name an implementation",
						relative, path)
				}
			}
		}
	}
}

// The domain is the one package with no imports of ours at all. Validation
// that needs a store is not validation.
func TestTheDomainImportsNothingOfOurs(t *testing.T) {
	t.Parallel()

	const domain = "../../internal/session"
	if _, err := os.Stat(domain); os.IsNotExist(err) {
		// Announced rather than silently passing: a rule that cannot yet be
		// broken is not a rule that is being kept.
		t.Skip("internal/session does not exist yet; this begins enforcing when it does")
	}

	for dir, paths := range imports(t, domain) {
		for _, path := range paths {
			if strings.HasPrefix(path, module) {
				t.Errorf("%s imports %s; the domain depends on nothing", dir, path)
			}
		}
	}
}
