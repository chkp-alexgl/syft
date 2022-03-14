package packages

import (
	"fmt"
	"github.com/anchore/syft/internal/log"
	"github.com/anchore/syft/syft/artifact"
	"github.com/anchore/syft/syft/cpe"
	"github.com/anchore/syft/syft/event/monitor"
	"github.com/anchore/syft/syft/linux"
	"github.com/anchore/syft/syft/pkg"
	"github.com/anchore/syft/syft/source"
	"github.com/hashicorp/go-multierror"
)

// Catalog a given source (container image or filesystem) with the given catalogers, returning all discovered packages.
// In order to efficiently retrieve contents from an underlying container image the content fetch requests are
// done in bulk. Specifically, all files of interest are collected from each cataloger and accumulated into a single
// request.
func Catalog(resolver source.FileResolver, release *linux.Release, catalogers ...pkg.Cataloger) (*pkg.Catalog, []artifact.Relationship, error) {
	catalog := pkg.NewCatalog()
	var allRelationships []artifact.Relationship

	filesProcessed, packagesDiscovered := monitor.NewPackageCatalogerMonitor()

	// perform analysis, accumulating errors for each failed analysis
	var errs error
	for _, c := range catalogers {
		// find packages from the underlying raw data
		log.Debugf("cataloging with %q", c.Name())
		packages, relationships, err := c.Catalog(resolver)
		if err != nil {
			errs = multierror.Append(errs, err)
			continue
		}

		catalogedPackages := len(packages)

		log.Debugf("discovered %d packages", catalogedPackages)
		packagesDiscovered.N += int64(catalogedPackages)

		for _, p := range packages {
			// generate CPEs (note: this is excluded from package ID, so is safe to mutate)
			p.CPEs = cpe.Generate(p)

			// generate PURL (note: this is excluded from package ID, so is safe to mutate)
			p.PURL = pkg.URL(p, release)

			// create file-to-package relationships for files owned by the package
			owningRelationships, err := packageFileOwnershipRelationships(p, resolver)
			if err != nil {
				log.Warnf("unable to create any package-file relationships for package name=%q: %w", p.Name, err)
			} else {
				allRelationships = append(allRelationships, owningRelationships...)
			}

			// add to catalog
			catalog.Add(p)
		}

		allRelationships = append(allRelationships, relationships...)
	}

	allRelationships = append(allRelationships, pkg.NewRelationships(catalog)...)

	if errs != nil {
		return nil, nil, errs
	}

	filesProcessed.SetCompleted()
	packagesDiscovered.SetCompleted()

	return catalog, allRelationships, nil
}

func packageFileOwnershipRelationships(p pkg.Package, resolver source.FilePathResolver) ([]artifact.Relationship, error) {
	fileOwner, ok := p.Metadata.(pkg.FileOwner)
	if !ok {
		return nil, nil
	}

	var relationships []artifact.Relationship

	for _, path := range fileOwner.OwnedFiles() {
		locations, err := resolver.FilesByPath(path)
		if err != nil {
			return nil, fmt.Errorf("unable to find path for path=%q: %w", path, err)
		}

		if len(locations) == 0 {
			// ideally we want to warn users about missing files from a package, however, it is very common for
			// container image authors to delete files that are not needed in order to keep image sizes small. Adding
			// a warning here would be needlessly noisy (even for popular base images).
			continue
		}

		for _, l := range locations {
			relationships = append(relationships, artifact.Relationship{
				From: p,
				To:   l.Coordinates,
				Type: artifact.ContainsRelationship,
			})
		}
	}

	return relationships, nil
}