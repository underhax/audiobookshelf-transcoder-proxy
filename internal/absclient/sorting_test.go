package absclient

import (
	"strings"
	"testing"
)

func TestParseSequenceNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		want  *float64
		name  string
		input string
	}{
		{
			name:  "empty string",
			input: "",
			want:  nil,
		},
		{
			name:  "whitespace only",
			input: "   ",
			want:  nil,
		},
		{
			name:  "plain integer",
			input: "1",
			want:  new(1.0),
		},
		{
			name:  "plain decimal",
			input: "2.5",
			want:  new(2.5),
		},
		{
			name:  "range notation",
			input: "1-3",
			want:  new(1.0),
		},
		{
			name:  "decimal range notation",
			input: "1.5-2.5",
			want:  new(1.5),
		},
		{
			name:  "dot without leading digit",
			input: ".5",
			want:  new(0.5),
		},
		{
			name:  "dot followed by letters",
			input: ".foo",
			want:  nil,
		},
		{
			name:  "non numeric string",
			input: "Volume One",
			want:  nil,
		},
		{
			name:  "number with trailing alphanumeric suffix",
			input: "10a",
			want:  new(10.0),
		},
		{
			name:  "multiple dots in sequence",
			input: "1.2.3",
			want:  new(1.2),
		},
		{
			name:  "numeric overflow fallback",
			input: strings.Repeat("9", 400) + "-overflow",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseSequenceNumber(tt.input)
			if got == nil && tt.want != nil {
				t.Fatalf("parseSequenceNumber(%q) = nil, want %v", tt.input, *tt.want)
			}
			if got != nil && tt.want == nil {
				t.Fatalf("parseSequenceNumber(%q) = %v, want nil", tt.input, *got)
			}
			if got != nil && tt.want != nil && *got != *tt.want {
				t.Fatalf("parseSequenceNumber(%q) = %v, want %v", tt.input, *got, *tt.want)
			}
		})
	}
}

func TestParseSeriesName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		wantName string
		wantSeq  string
	}{
		{
			name:     "empty string",
			input:    "",
			wantName: "",
			wantSeq:  "",
		},
		{
			name:     "whitespace only",
			input:    "   ",
			wantName: "",
			wantSeq:  "",
		},
		{
			name:     "name with sequence",
			input:    "Series One #1",
			wantName: "Series One",
			wantSeq:  "1",
		},
		{
			name:     "name with decimal sequence",
			input:    "Decimal Series #11.2",
			wantName: "Decimal Series",
			wantSeq:  "11.2",
		},
		{
			name:     "name without sequence",
			input:    "Dune",
			wantName: "Dune",
			wantSeq:  "",
		},
		{
			name:     "name with multiple hashes takes last hash",
			input:    "Project #9 #3",
			wantName: "Project #9",
			wantSeq:  "3",
		},
		{
			name:     "name with trailing hash space",
			input:    "Empty Hash #",
			wantName: "Empty Hash",
			wantSeq:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotName, gotSeq := parseSeriesName(tt.input)
			if gotName != tt.wantName {
				t.Errorf("parseSeriesName(%q) gotName = %q, want %q", tt.input, gotName, tt.wantName)
			}
			if gotSeq != tt.wantSeq {
				t.Errorf("parseSeriesName(%q) gotSeq = %q, want %q", tt.input, gotSeq, tt.wantSeq)
			}
		})
	}
}

func TestExtractSeries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		lookup       map[string]seriesLookupEntry
		name         string
		itemID       string
		seriesName   string
		seriesSeq    string
		wantSeries   string
		wantSeriesID string
		wantSeq      string
		seriesList   []rawSeriesEntry
	}{
		{
			name: "primary series present in list",
			seriesList: []rawSeriesEntry{
				{
					ID:       "ser-101",
					Name:     "Primary Series",
					Sequence: "3",
				},
			},
			itemID:       "b-1",
			lookup:       nil,
			seriesName:   "Fallback Series A",
			seriesSeq:    "1",
			wantSeries:   "Primary Series",
			wantSeriesID: "ser-101",
			wantSeq:      "3",
		},
		{
			name: "primary series in list without sequence uses seriesSeq",
			seriesList: []rawSeriesEntry{
				{
					ID:   "ser-102",
					Name: "Primary Series 2",
				},
			},
			itemID:       "b-2",
			lookup:       nil,
			seriesName:   "Fallback Series B #5",
			seriesSeq:    "4",
			wantSeries:   "Primary Series 2",
			wantSeriesID: "ser-102",
			wantSeq:      "4",
		},
		{
			name: "primary series in list without sequence falls back to parsed seriesName",
			seriesList: []rawSeriesEntry{
				{
					ID:   "ser-103",
					Name: "Primary Series 3",
				},
			},
			itemID:       "b-3",
			lookup:       nil,
			seriesName:   "Primary Series 3 #7",
			seriesSeq:    "",
			wantSeries:   "Primary Series 3",
			wantSeriesID: "ser-103",
			wantSeq:      "7",
		},
		{
			name:       "series resolved via lookup map and seriesName sequence",
			seriesList: nil,
			itemID:     "b-4",
			lookup: map[string]seriesLookupEntry{
				"b-4": {id: "ser-lookup-1", name: "Lookup Series"},
			},
			seriesName:   "Lookup Series #2",
			seriesSeq:    "",
			wantSeries:   "Lookup Series",
			wantSeriesID: "ser-lookup-1",
			wantSeq:      "2",
		},
		{
			name:       "series resolved via lookup map with explicit seriesSeq",
			seriesList: nil,
			itemID:     "b-5",
			lookup: map[string]seriesLookupEntry{
				"b-5": {id: "ser-lookup-2", name: "Lookup Saga"},
			},
			seriesName:   "Ignored Fallback",
			seriesSeq:    "8",
			wantSeries:   "Lookup Saga",
			wantSeriesID: "ser-lookup-2",
			wantSeq:      "8",
		},
		{
			name:         "empty series list and missing lookup falls back to parsed seriesName with hash",
			seriesList:   nil,
			itemID:       "b-6",
			lookup:       map[string]seriesLookupEntry{},
			seriesName:   "Fallback Series C #9",
			seriesSeq:    "",
			wantSeries:   "Fallback Series C",
			wantSeriesID: "",
			wantSeq:      "9",
		},
		{
			name:         "empty series list and missing lookup with raw name and no sequence",
			seriesList:   nil,
			itemID:       "b-7",
			lookup:       nil,
			seriesName:   "Standalone Series",
			seriesSeq:    "",
			wantSeries:   "Standalone Series",
			wantSeriesID: "",
			wantSeq:      "",
		},
		{
			name:         "empty inputs produce empty outputs",
			seriesList:   nil,
			itemID:       "",
			lookup:       nil,
			seriesName:   "",
			seriesSeq:    "",
			wantSeries:   "",
			wantSeriesID: "",
			wantSeq:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			series, seriesID, seq := extractSeries(tt.seriesList, tt.itemID, tt.lookup, tt.seriesName, tt.seriesSeq)
			if series != tt.wantSeries {
				t.Errorf("series = %q, want %q", series, tt.wantSeries)
			}
			if seriesID != tt.wantSeriesID {
				t.Errorf("seriesID = %q, want %q", seriesID, tt.wantSeriesID)
			}
			if seq != tt.wantSeq {
				t.Errorf("seq = %q, want %q", seq, tt.wantSeq)
			}
		})
	}
}

func TestSortMediaItems(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		items     []MediaItem
		wantOrder []string
	}{
		{
			name: "series sorted by series name then sequence then title",
			items: []MediaItem{
				{ID: "m-3", Series: "Series Dune", SequenceNum: new(2.0), Title: "Children"},
				{ID: "m-1", Series: "Series Dune", SequenceNum: new(1.0), Title: "Original"},
				{ID: "m-4", Series: "Series Mars", SequenceNum: nil, Title: "Z Chapter"},
				{ID: "m-2", Series: "Series Mars", SequenceNum: nil, Title: "A Chapter"},
				{ID: "m-5", Series: "Series Foundation", SequenceNum: new(1.0), Title: "Empire"},
				{ID: "m-6", Series: "Series Foundation", SequenceNum: new(2.0), Title: "Another Empire"},
			},
			wantOrder: []string{"m-1", "m-3", "m-5", "m-6", "m-2", "m-4"},
		},
		{
			name: "series vs standalone book with matching and differing names",
			items: []MediaItem{
				{ID: "standalone-b", Author: "Bravo Author", Title: "Solo B"},
				{ID: "series-b", Series: "Bravo Author", SequenceNum: new(1.0), Title: "Series Solo"},
				{ID: "series-a", Series: "Alpha Series", SequenceNum: new(1.0), Title: "Series Alpha"},
				{ID: "standalone-z", Author: "Zulu Author", Title: "Solo Z"},
			},
			wantOrder: []string{"series-a", "series-b", "standalone-b", "standalone-z"},
		},
		{
			name: "standalone books sorted by author then title",
			items: []MediaItem{
				{ID: "s-3", Author: "Author B", Title: "Title 2"},
				{ID: "s-1", Author: "Author A", Title: "Title 1"},
				{ID: "s-2", Author: "Author B", Title: "Title 1"},
			},
			wantOrder: []string{"s-1", "s-2", "s-3"},
		},
		{
			name: "standalone compared against series matching name",
			items: []MediaItem{
				{ID: "stand-same", Author: "Arthur", Title: "Solo Same"},
				{ID: "ser-same", Series: "Arthur", SequenceNum: new(1.0), Title: "Series Same"},
			},
			wantOrder: []string{"ser-same", "stand-same"},
		},
		{
			name: "standalone compared against series differing name",
			items: []MediaItem{
				{ID: "stand-diff", Author: "Wells", Title: "Solo Diff"},
				{ID: "ser-diff", Series: "Verne", SequenceNum: new(1.0), Title: "Series Diff"},
			},
			wantOrder: []string{"ser-diff", "stand-diff"},
		},
		{
			name: "standalone author less than series name",
			items: []MediaItem{
				{ID: "stand-less", Author: "Clarke", Title: "Solo Less"},
				{ID: "ser-more", Series: "Gibson", SequenceNum: new(1.0), Title: "Series More"},
			},
			wantOrder: []string{"stand-less", "ser-more"},
		},
		{
			name: "three items permutation standalone and series",
			items: []MediaItem{
				{ID: "ser-z", Series: "Tolkien", SequenceNum: new(1.0), Title: "Series Z"},
				{ID: "ser-a", Series: "Lewis", SequenceNum: new(1.0), Title: "Series A"},
				{ID: "stand-a", Author: "Lewis", Title: "Solo A"},
			},
			wantOrder: []string{"ser-a", "stand-a", "ser-z"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			items := make([]MediaItem, len(tt.items))
			copy(items, tt.items)
			sortMediaItems(items)
			for i, id := range tt.wantOrder {
				if items[i].ID != id {
					t.Errorf("index %d: got %s, want %s", i, items[i].ID, id)
				}
			}
		})
	}
}

func TestSortPodcastEpisodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		episodes  []PodcastEpisode
		wantOrder []string
	}{
		{
			name: "sorted descending by publishedAt",
			episodes: []PodcastEpisode{
				{ID: "ep-old", PublishedAt: "2026-01-01T00:00:00Z", Title: "Old"},
				{ID: "ep-new", PublishedAt: "2026-03-01T00:00:00Z", Title: "New"},
				{ID: "ep-mid", PublishedAt: "2026-02-01T00:00:00Z", Title: "Mid"},
			},
			wantOrder: []string{"ep-new", "ep-mid", "ep-old"},
		},
		{
			name: "sorted descending by publishedAt two items reversed",
			episodes: []PodcastEpisode{
				{ID: "ep-early", PublishedAt: "2026-04-01T00:00:00Z", Title: "Early"},
				{ID: "ep-late", PublishedAt: "2026-05-01T00:00:00Z", Title: "Late"},
			},
			wantOrder: []string{"ep-late", "ep-early"},
		},
		{
			name: "publishedAt priority over missing publishedAt",
			episodes: []PodcastEpisode{
				{ID: "ep-no-date", PublishedAt: "", EpisodeNum: new(99.0), Title: "No Date"},
				{ID: "ep-with-date", PublishedAt: "2026-06-01T00:00:00Z", EpisodeNum: new(1.0), Title: "With Date"},
			},
			wantOrder: []string{"ep-with-date", "ep-no-date"},
		},
		{
			name: "publishedAt priority reversed order",
			episodes: []PodcastEpisode{
				{ID: "ep-with-date-2", PublishedAt: "2026-07-01T00:00:00Z", EpisodeNum: new(1.0), Title: "With Date 2"},
				{ID: "ep-no-date-2", PublishedAt: "", EpisodeNum: new(98.0), Title: "No Date 2"},
			},
			wantOrder: []string{"ep-with-date-2", "ep-no-date-2"},
		},
		{
			name: "three episodes publishedAt mixed with empty date",
			episodes: []PodcastEpisode{
				{ID: "ep-empty-date", PublishedAt: "", EpisodeNum: new(5.0), Title: "Empty Date"},
				{ID: "ep-date-1", PublishedAt: "2026-08-01T00:00:00Z", EpisodeNum: new(1.0), Title: "Date 1"},
				{ID: "ep-date-2", PublishedAt: "2026-09-01T00:00:00Z", EpisodeNum: new(2.0), Title: "Date 2"},
			},
			wantOrder: []string{"ep-date-2", "ep-date-1", "ep-empty-date"},
		},
		{
			name: "equal date sorted descending by episodeNum then title",
			episodes: []PodcastEpisode{
				{ID: "ep-1", PublishedAt: "2026-10-01T00:00:00Z", EpisodeNum: new(1.0), Title: "Ep 1"},
				{ID: "ep-2", PublishedAt: "2026-10-01T00:00:00Z", EpisodeNum: new(2.0), Title: "Ep 2"},
			},
			wantOrder: []string{"ep-2", "ep-1"},
		},
		{
			name: "equal empty date tie break by title and nil episode number",
			episodes: []PodcastEpisode{
				{ID: "ep-nil-2", PublishedAt: "", EpisodeNum: nil, Title: "Z Nil"},
				{ID: "ep-nil-1", PublishedAt: "", EpisodeNum: nil, Title: "A Nil"},
				{ID: "ep-2-dup", PublishedAt: "", EpisodeNum: new(2.0), Title: "Another Ep 2"},
			},
			wantOrder: []string{"ep-2-dup", "ep-nil-1", "ep-nil-2"},
		},
		{
			name: "equal date descending episodeNum two items reversed",
			episodes: []PodcastEpisode{
				{ID: "ep-low", PublishedAt: "2026-11-01T00:00:00Z", EpisodeNum: new(1.0), Title: "Low"},
				{ID: "ep-high", PublishedAt: "2026-11-01T00:00:00Z", EpisodeNum: new(2.0), Title: "High"},
			},
			wantOrder: []string{"ep-high", "ep-low"},
		},
		{
			name: "empty date descending episodeNum three items reversed",
			episodes: []PodcastEpisode{
				{ID: "ep-n3", PublishedAt: "", EpisodeNum: new(3.0), Title: "Three"},
				{ID: "ep-n2", PublishedAt: "", EpisodeNum: new(2.0), Title: "Two"},
				{ID: "ep-n1", PublishedAt: "", EpisodeNum: new(1.0), Title: "One"},
			},
			wantOrder: []string{"ep-n3", "ep-n2", "ep-n1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			episodes := make([]PodcastEpisode, len(tt.episodes))
			copy(episodes, tt.episodes)
			sortPodcastEpisodes(episodes)
			for i, id := range tt.wantOrder {
				if episodes[i].ID != id {
					t.Errorf("index %d: got %s, want %s", i, episodes[i].ID, id)
				}
			}
		})
	}
}

func TestCompareSequenceNums(t *testing.T) {
	t.Parallel()

	tests := []struct {
		a    *float64
		b    *float64
		name string
		want int
	}{
		{name: "a less than b", a: new(1.0), b: new(2.0), want: -1},
		{name: "a greater than b", a: new(2.0), b: new(1.0), want: 1},
		{name: "a equal to b", a: new(1.0), b: new(1.0), want: 0},
		{name: "a present b nil", a: new(1.0), b: nil, want: -1},
		{name: "a nil b present", a: nil, b: new(1.0), want: 1},
		{name: "both nil", a: nil, b: nil, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := compareSequenceNums(tt.a, tt.b); got != tt.want {
				t.Errorf("compareSequenceNums() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestComparePublishedAt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a    string
		b    string
		want int
	}{
		{name: "a newer than b", a: "2025-02-01T00:00:00Z", b: "2025-01-01T00:00:00Z", want: -1},
		{name: "a older than b", a: "2025-01-01T00:00:00Z", b: "2025-02-01T00:00:00Z", want: 1},
		{name: "both equal dates", a: "2025-03-01T00:00:00Z", b: "2025-03-01T00:00:00Z", want: 0},
		{name: "a present b empty", a: "2025-04-01T00:00:00Z", b: "", want: -1},
		{name: "a empty b present", a: "", b: "2025-05-01T00:00:00Z", want: 1},
		{name: "both empty", a: "", b: "", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := comparePublishedAt(tt.a, tt.b); got != tt.want {
				t.Errorf("comparePublishedAt() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCompareEpisodeNums(t *testing.T) {
	t.Parallel()

	tests := []struct {
		a    *float64
		b    *float64
		name string
		want int
	}{
		{name: "a greater than b", a: new(2.0), b: new(1.0), want: -1},
		{name: "a less than b", a: new(1.0), b: new(2.0), want: 1},
		{name: "both equal numbers", a: new(1.0), b: new(1.0), want: 0},
		{name: "a present b nil", a: new(1.0), b: nil, want: -1},
		{name: "a nil b present", a: nil, b: new(1.0), want: 1},
		{name: "both nil", a: nil, b: nil, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := compareEpisodeNums(tt.a, tt.b); got != tt.want {
				t.Errorf("compareEpisodeNums() = %d, want %d", got, tt.want)
			}
		})
	}
}
