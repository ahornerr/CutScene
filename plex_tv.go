package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"github.com/google/uuid"
	"net/http"
)

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
	url := fmt.Sprintf("https://plex.tv/api/users?X-Plex-Token=%s&X-Plex-Client-Identifier=%s", p.token, p.identifier)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	var users Users
	if err := xml.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, err
	}

	return &users, nil
}

type User struct {
	Id       int    `json:"id"`
	Uuid     string `json:"uuid"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

type GetUserResp struct {
	User User `json:"user"`
}

func (p *PlexTV) getUser() (*User, error) {
	req, err := http.NewRequest(http.MethodGet, "https://plex.tv/users/account.json", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-Plex-Token", p.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	var userResp GetUserResp
	if err := json.NewDecoder(resp.Body).Decode(&userResp); err != nil {
		return nil, err
	}

	return &userResp.User, nil
}
