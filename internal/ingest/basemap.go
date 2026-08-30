package ingest

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

// BasemapSpec pins the source archive and every parameter needed to reproduce it.
type BasemapSpec struct {
	Source      string
	Tool        string
	BBox        string
	MaxZoom     int
	Overfetch   int
	Filename    string
	Size        int64
	SHA256      string
	Attribution string
}

// ProductionBasemap is a global zoom 0-6 extract using Protomaps schema 4.15.2.
var ProductionBasemap = BasemapSpec{
	Source:      "https://build.protomaps.com/20260829.pmtiles",
	Tool:        "github.com/protomaps/go-pmtiles@v1.30.0",
	BBox:        "-180,-85.0511,180,85.0511",
	MaxZoom:     6,
	Overfetch:   0,
	Filename:    "protomaps-20260829-z0-6.pmtiles",
	Size:        44_856_992,
	SHA256:      "9a6cd0b9b26b4bcf13fb3167755431816058659552c0f33f5d1df3793d093082",
	Attribution: `<a href="https://github.com/protomaps/basemaps">Protomaps</a> · © <a href="https://www.openstreetmap.org/copyright">OpenStreetMap contributors</a> · <a href="https://docs.overturemaps.org/attribution/">© ESA WorldCover project 2020 / Contains modified Copernicus Sentinel data (2020) processed by ESA WorldCover consortium</a> (<a href="https://creativecommons.org/licenses/by/4.0/">CC BY 4.0</a>)`,
}

// Key is the immutable dataset-relative artifact key.
func (s BasemapSpec) Key() string {
	return filepath.ToSlash(filepath.Join("basemap", s.Filename))
}

// VerifyBasemap returns the archive only when its size and digest match the pin.
func VerifyBasemap(dir string, spec BasemapSpec) ([]byte, error) {
	path := filepath.Join(dir, spec.Filename)
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("verify basemap %s: %w", path, err)
	}
	if int64(len(body)) != spec.Size {
		return nil, fmt.Errorf("verify basemap %s: size %d, want %d", path, len(body), spec.Size)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	if digest != spec.SHA256 {
		return nil, fmt.Errorf("verify basemap %s: sha256 %s, want %s", path, digest, spec.SHA256)
	}
	return body, nil
}
