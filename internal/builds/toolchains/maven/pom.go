package maven

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
)

// MaxPOMSize is the largest pom.xml ParsePOM accepts.
const MaxPOMSize = 4 << 20

type pomXML struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
	Parent     struct {
		GroupID string `xml:"groupId"`
		Version string `xml:"version"`
	} `xml:"parent"`
	Modules      []string `xml:"modules>module"`
	Dependencies []struct {
		GroupID    string `xml:"groupId"`
		ArtifactID string `xml:"artifactId"`
		Version    string `xml:"version"`
	} `xml:"dependencies>dependency"`
}

// ParsePOM reads one pom.xml: its coordinate (group and version fall back to
// the parent's) and its direct dependencies. It never resolves properties,
// parents or repositories and never touches the network. It rejects
// malformed XML, a missing artifactId and input over 4 MiB.
func ParsePOM(r io.Reader) (corebuilds.Module, error) {
	m, _, err := parsePOM(r)
	return m, err
}

func parsePOM(r io.Reader) (corebuilds.Module, []string, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxPOMSize+1))
	if err != nil {
		return corebuilds.Module{}, nil, fmt.Errorf("read pom.xml: %w", err)
	}
	if len(data) > MaxPOMSize {
		return corebuilds.Module{}, nil, errors.New("pom.xml is larger than 4 MiB; Yggdrasil will not read it")
	}
	var p pomXML
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.CharsetReader = charsetReader
	if err := dec.Decode(&p); err != nil {
		var ce charsetError
		if errors.As(err, &ce) {
			return corebuilds.Module{}, nil, fmt.Errorf("pom.xml is encoded as %q; Yggdrasil reads UTF-8 and ISO-8859-1. Save the file as UTF-8", string(ce))
		}
		return corebuilds.Module{}, nil, fmt.Errorf("pom.xml is not valid XML: %w", err)
	}
	p.ArtifactID = strings.TrimSpace(p.ArtifactID)
	if p.ArtifactID == "" {
		return corebuilds.Module{}, nil, errors.New("pom.xml has no artifactId")
	}
	group := strings.TrimSpace(p.GroupID)
	if group == "" {
		group = strings.TrimSpace(p.Parent.GroupID)
	}
	version := strings.TrimSpace(p.Version)
	if version == "" {
		version = strings.TrimSpace(p.Parent.Version)
	}
	m := corebuilds.Module{Coord: corebuilds.Coord{Group: group, Artifact: p.ArtifactID, Version: version}}
	for _, d := range p.Dependencies {
		a := strings.TrimSpace(d.ArtifactID)
		if a == "" {
			continue
		}
		m.Deps = append(m.Deps, corebuilds.Coord{Group: strings.TrimSpace(d.GroupID), Artifact: a, Version: strings.TrimSpace(d.Version)})
	}
	var mods []string
	for _, s := range p.Modules {
		if s = strings.TrimSpace(s); s != "" {
			mods = append(mods, s)
		}
	}
	return m, mods, nil
}
