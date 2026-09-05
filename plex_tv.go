package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
)

var plexTVHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
}

type PlexTV struct {
	token      string
	identifier string
}

func NewPlexTV(token string) *PlexTV {
	return &PlexTV{
		token:      token,
		identifier: uuid.New().String(),
	}
}

type Users struct {
	XMLName           xml.Name `xml:"MediaContainer"`
	MachineIdentifier string   `xml:"machineIdentifier,attr"`
	User              []struct {
		ID        string `xml:"id,attr"`
		Username  string `xml:"username,attr"`
		Email     string `xml:"email,attr"`
		AllowSync string `xml:"allowSync,attr"`
		Server    []struct {
			ID                string `xml:"id,attr"`
			ServerId          string `xml:"serverId,attr"`
			MachineIdentifier string `xml:"machineIdentifier,attr"`
			Name              string `xml:"name,attr"`
			Owned             string `xml:"owned,attr"`   // When this is "0", it's someone else's server
			Pending           string `xml:"pending,attr"` // Guessing when this is 1, the invite hasn't been accepted
		} `xml:"Server"`
	} `xml:"User"`
}

func (u Users) HasUser(userId string, machineId string) bool {
	for _, user := range u.User {
		if user.ID == userId {
			for _, server := range user.Server {
				if server.MachineIdentifier == machineId {
					return true
				}
			}
		}
	}

	return false
}

func (p *PlexTV) getUsers() (*Users, error) {
	return p.getUsersContext(context.Background())
}

func (p *PlexTV) getUsersContext(ctx context.Context) (*Users, error) {
	reqUrl := "https://plex.tv/api/users"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqUrl, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Plex-Token", p.token)
	if p.identifier != "" {
		req.Header.Set("X-Plex-Client-Identifier", p.identifier)
	}

	resp, err := plexTVHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("plex.tv users returned status %d: %s", resp.StatusCode, string(body))
	}

	var users Users
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&users); err != nil {
		return nil, err
	}

	return &users, nil
}

type User struct {
	Id       int    `json:"id"`
	Uuid     string `json:"uuid"`
	Username string `json:"username"`
	Title    string `json:"title"`
	Email    string `json:"email"`
}

// Plex has returned account IDs as both JSON numbers and quoted numbers over
// time. Keep the application identity numeric while accepting either wire
// representation.
func (u *User) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID       json.RawMessage `json:"id"`
		UUID     string          `json:"uuid"`
		Username string          `json:"username"`
		Title    string          `json:"title"`
		Email    string          `json:"email"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	id, err := plexNumericID(raw.ID)
	if err != nil {
		return fmt.Errorf("invalid Plex user id: %w", err)
	}
	if int64(int(id)) != id {
		return fmt.Errorf("Plex user id %d does not fit in int", id)
	}
	u.Id = int(id)
	u.Uuid = raw.UUID
	u.Username = raw.Username
	u.Title = raw.Title
	u.Email = raw.Email
	return nil
}

func plexNumericID(raw json.RawMessage) (int64, error) {
	text, err := plexNumericIDText(raw)
	if err != nil || text == "" {
		return 0, err
	}
	return strconv.ParseInt(text, 10, 64)
}

func plexNumericIDText(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}

	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", err
		}
		return text, nil
	}

	return string(raw), nil
}

type GetUserResp struct {
	User User `json:"user"`
}

func (p *PlexTV) getUser() (*User, error) {
	return p.getUserContext(context.Background())
}

func (p *PlexTV) getUserContext(ctx context.Context) (*User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://plex.tv/users/account.json", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-Plex-Token", p.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := plexTVHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("plex.tv account returned status %d: %s", resp.StatusCode, string(body))
	}

	var userResp GetUserResp
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&userResp); err != nil {
		return nil, err
	}

	return &userResp.User, nil
}
