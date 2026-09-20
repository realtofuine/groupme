package groupmeext

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/beeper/groupme-lib"
)

type Message struct{ groupme.Message }

func (m *Message) Scan(value interface{}) error {
	bytes, ok := value.(string)
	if !ok {
		return errors.New(fmt.Sprint("Failed to unmarshal json value:", value))
	}

	message := Message{}
	err := json.Unmarshal([]byte(bytes), &message)

	*m = Message(message)
	return err
}

func (m *Message) Value() (driver.Value, error) {
	e, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return e, nil
}

// DownloadImage downloads an image attachment from GroupMe's image CDN
// (i.groupme.com), a plain unauthenticated GET.
func DownloadImage(url string) (data *[]byte, mime string, err error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, "", fmt.Errorf("failed to download image: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read downloaded image: %w", err)
	}

	mime = resp.Header.Get("Content-Type")
	if mime == "" {
		mime = http.DetectContentType(body)
	}
	return &body, mime, nil
}

// DownloadVideo downloads a video attachment. Unlike images (a plain
// public GET) and files (a signed X-Access-Token API call, see
// DownloadFile), GroupMe's video CDN authenticates via a "token" cookie
// carrying the account's access token -- ported as-is from the pre-2023
// bridge (the only place this was ever verified to work against the real
// API); not separately re-verified live in this revival, see NOTES.md.
func DownloadVideo(videoURL, token string) (data []byte, mime string, err error) {
	req, err := http.NewRequest(http.MethodGet, videoURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to build video request: %w", err)
	}
	req.AddCookie(&http.Cookie{Name: "token", Value: token})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("failed to download video: %w", err)
	}
	defer resp.Body.Close()

	data, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read downloaded video: %w", err)
	}

	mime = resp.Header.Get("Content-Type")
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	return data, mime, nil
}

// fileMetadata is the response shape of file.groupme.com's "fileData"
// lookup endpoint (see DownloadFile).
type fileMetadata struct {
	FileData struct {
		FileName string `json:"file_name"`
		FileSize int    `json:"file_size"`
		Mime     string `json:"mime_type"`
	} `json:"file_data"`
}

// DownloadFile downloads a "file" attachment (GroupMe's group file-sharing
// feature, distinct from image/video message attachments) via
// file.groupme.com. This is a two-step API, both authenticated via
// X-Access-Token: one call resolves the file's name/mime type, a second
// fetches its bytes. groupID is the containing group's ID -- GroupMe's
// file-sharing feature is group-only, so this isn't expected to be called
// for a DM attachment.
//
// Ported from the pre-2023 bridge's equivalent, which used to panic() on
// any request error here -- a network hiccup on a single file attachment
// would have taken down the entire bridge process. Rewritten to return an
// error instead, same as every other attachment download path.
func DownloadFile(groupID groupme.ID, fileID, token string) (data []byte, filename, mime string, err error) {
	reqBody, err := json.Marshal(struct {
		FileIDs []string `json:"file_ids"`
	}{FileIDs: []string{fileID}})
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to build file metadata request body: %w", err)
	}

	metaReq, err := http.NewRequest(http.MethodPost, fmt.Sprintf("https://file.groupme.com/v1/%s/fileData", groupID), bytes.NewReader(reqBody))
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to build file metadata request: %w", err)
	}
	metaReq.Header.Set("X-Access-Token", token)
	metaReq.Header.Set("Content-Type", "application/json")

	metaResp, err := http.DefaultClient.Do(metaReq)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to fetch file metadata: %w", err)
	}
	defer metaResp.Body.Close()

	var meta []fileMetadata
	if err := json.NewDecoder(metaResp.Body).Decode(&meta); err != nil {
		return nil, "", "", fmt.Errorf("failed to decode file metadata: %w", err)
	}
	if len(meta) == 0 {
		return nil, "", "", fmt.Errorf("GroupMe returned no metadata for file %s", fileID)
	}

	dlReq, err := http.NewRequest(http.MethodPost, fmt.Sprintf("https://file.groupme.com/v1/%s/files/%s", groupID, fileID), nil)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to build file download request: %w", err)
	}
	dlReq.Header.Set("X-Access-Token", token)

	dlResp, err := http.DefaultClient.Do(dlReq)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to download file: %w", err)
	}
	defer dlResp.Body.Close()

	data, err = io.ReadAll(dlResp.Body)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to read downloaded file: %w", err)
	}

	return data, meta[0].FileData.FileName, meta[0].FileData.Mime, nil
}
