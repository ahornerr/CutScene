<?php




class PlexTV {    public $token;
    public $identifier;
}

function NewPlexTV($token) {
	return &PlexTV{
		token:      token,
		identifier: $uuid->New().String(),
	}
}

class Users {    public $XMLName;
    public $MachineIdentifier;
    public $User;
    public $ID;
    public $Username;
    public $Email;
    public $AllowSync;
    public $Server;
    public $ID;
    public $ServerId;
    public $MachineIdentifier;
    public $Name;
    public $Owned;
    public $Pending;
		} `xml:"Server"`
	} `xml:"User"`
}

public function HasUser($userId, $machineId) {list($for, $_, $user) = range $u->User {
		if $user->ID == userId {list($for, $_, $server) = range $user->Server {
				if $server->MachineIdentifier == machineId {
					return true
				}
			}
		}
	}

	return false
}

public function getUsers() {$url = sprintf("https://$plex->tv/api/users?X-Plex-Token=%s&X-Plex-Client-Identifier=%s", $p->token, $p->identifier)list($resp, $err) = $http->Get(url)
	if $err !== null {
		return null, err
	}

	defer $resp->Body.Close()list($var, $users, $Users, $if, $err) = $xml->NewDecoder($resp->Body).Decode(&users); $err !== null {
		return null, err
	}

	return &users, null
}

class User {    public $Id;
    public $Uuid;
    public $Username;
    public $Title;
    public $Email;
}

// Plex has returned account IDs as both JSON numbers and quoted numbers over
// time. Keep the application identity numeric while accepting either wire
// representation.
public function UnmarshalJSON($data) {
	$raw = null; {
		ID       $json->RawMessage `json:"id"`
		UUID     string          `json:"uuid"`
		Username string          `json:"username"`
		Title    string          `json:"title"`
		Email    string          `json:"email"`
	}list($if, $err) = $json->Unmarshal(data, &raw); $err !== null {
		return err
	}list($id, $err) = plexNumericID($raw->ID)
	if $err !== null {
		return $fmt->Errorf("invalid Plex user id: %w", err)
	}
	if int64(int(id)) != id {
		return $fmt->Errorf("Plex user id %d does not fit in int", id)
	}
	$u->Id = int(id)
	$u->Uuid = $raw->UUID
	$u->Username = $raw->Username
	$u->Title = $raw->Title
	$u->Email = $raw->Email
	return null
}

function plexNumericID($$raw->RawMessage) {list($text, $err) = plexNumericIDText(raw)
	if $err !== null || text == "" {
		return 0, err
	}
	return $strconv->ParseInt(text, 10, 64)
}

function plexNumericIDText($$raw->RawMessage) {
	raw = $bytes->TrimSpace(raw)
	if len(raw) == 0 || $bytes->Equal(raw, []byte("null")) {
		return "", null
	}

	if raw[0] == '"' {list($var, $text, $string, $if, $err) = $json->Unmarshal(raw, &text); $err !== null {
			return "", err
		}
		return text, null
	}

	return string(raw), null
}

class GetUserResp {    public $User;
}

public function getUser() {list($req, $err) = $http->NewRequest($http->MethodGet, "https://$plex->tv/users/$account->json", null)
	if $err !== null {
		return null, err
	}

	$req->Header.Set("X-Plex-Token", $p->token)
	$req->Header.Set("Content-Type", "application/json")
	$req->Header.Set("Accept", "application/json")list($resp, $err) = $http->DefaultClient.Do(req)
	if $err !== null {
		return null, err
	}

	defer $resp->Body.Close()list($var, $userResp, $GetUserResp, $if, $err) = $json->NewDecoder($resp->Body).Decode(&userResp); $err !== null {
		return null, err
	}

	return &$userResp->User, null
}
