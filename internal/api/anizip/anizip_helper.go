package anizip

import "strings"

type Artwork struct {
	Fanart string `json:"fanart,omitempty"`
	Logo   string `json:"logo,omitempty"`
	Title  string `json:"title,omitempty"`
}

func (m *Media) GetArtwork() *Artwork {
	if m == nil {
		return &Artwork{}
	}
	a := &Artwork{Title: m.GetTitle()}
	for _, img := range m.Images {
		switch strings.ToLower(img.CoverType) {
		case "fanart":
			a.Fanart = img.URL
		case "clearlogo":
			a.Logo = img.URL
		}
	}
	return a
}

func (m *Media) GetTitle() string {
	if m == nil {
		return ""
	}
	if len(m.Titles["en"]) > 0 {
		return m.Titles["en"]
	}
	return m.Titles["ro"]
}

func (m *Media) GetMappings() *Mappings {
	if m == nil {
		return &Mappings{}
	}
	return m.Mappings
}

func (m *Media) FindEpisode(ep string) (*Episode, bool) {
	if m.Episodes == nil {
		return nil, false
	}
	episode, found := m.Episodes[ep]
	if !found {
		return nil, false
	}

	return &episode, true
}

func (m *Media) GetMainEpisodeCount() int {
	if m == nil {
		return 0
	}
	return m.EpisodeCount
}

// GetOffset returns the offset of the first episode relative to the absolute episode number.
// e.g, if the first episode's absolute number is 13, then the offset is 12.
func (m *Media) GetOffset() int {
	if m == nil {
		return 0
	}
	firstEp, found := m.FindEpisode("1")
	if !found {
		return 0
	}
	if firstEp.AbsoluteEpisodeNumber == 0 {
		return 0
	}
	return firstEp.AbsoluteEpisodeNumber - 1
}

func (e *Episode) GetTitle() string {
	eng, ok := e.Title["en"]
	if ok {
		return eng
	}
	rom, ok := e.Title["x-jat"]
	if ok {
		return rom
	}
	return ""
}
