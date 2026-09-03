package absclient

// DeviceInfo conveys player client metadata required by Audiobookshelf to identify the streaming device.
type DeviceInfo struct {
	DeviceID      string `json:"deviceId"`
	ClientName    string `json:"clientName"`
	ClientVersion string `json:"clientVersion"`
}

// PlayRequest defines configuration sent to Audiobookshelf when requesting direct-play audio stream paths.
type PlayRequest struct {
	DeviceInfo         DeviceInfo `json:"deviceInfo"`
	SupportedMimeTypes []string   `json:"supportedMimeTypes"`
	ForceDirectPlay    bool       `json:"forceDirectPlay"`
}

// AudioTrack defines segment offset and source URI metadata for multi-track audiobooks.
type AudioTrack struct {
	ContentURL  string  `json:"contentUrl"`
	Index       int     `json:"index"`
	StartOffset float64 `json:"startOffset"`
	Duration    float64 `json:"duration"`
}

// PlayResponse encapsulates the initial playback state and track inventory returned by Audiobookshelf.
type PlayResponse struct {
	MediaMetadata *struct {
		Title        string   `json:"title"`
		AuthorName   string   `json:"authorName"`
		Author       string   `json:"author"`
		NarratorName string   `json:"narratorName"`
		Narrators    []string `json:"narrators"`
	} `json:"mediaMetadata"`
	LibraryItem *struct {
		Media struct {
			Metadata struct {
				NarratorName string `json:"narratorName"`
			} `json:"metadata"`
		} `json:"media"`
	} `json:"libraryItem"`
	ID            string       `json:"id"`
	LibraryItemID string       `json:"libraryItemId"`
	DisplayTitle  string       `json:"displayTitle"`
	DisplayAuthor string       `json:"displayAuthor"`
	CoverPath     string       `json:"coverPath"`
	MediaType     string       `json:"mediaType"`
	AudioTracks   []AudioTrack `json:"audioTracks"`
	CurrentTime   float64      `json:"currentTime"`
	Duration      float64      `json:"duration"`
}

// SyncRequest conveys periodic playback progress and accumulated listening time to update server records.
type SyncRequest struct {
	CurrentTime  float64 `json:"currentTime"`
	TimeListened float64 `json:"timeListened"`
	Duration     float64 `json:"duration"`
}
