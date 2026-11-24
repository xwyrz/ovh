package hetzner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// QBStats qBittorrent 节点状态（只读查询，对照 qb_client.py fetch_stats）。
type QBStats struct {
	Enabled          bool   `json:"enabled"`
	DLSpeed          int64  `json:"dl_speed"`
	UPSpeed          int64  `json:"up_speed"`
	DLTotal          int64  `json:"dl_total"`
	UPTotal          int64  `json:"up_total"`
	AllTorrents      int    `json:"all_torrents"`
	ActiveTorrents   int    `json:"active_torrents"`
	DHTNodes         int    `json:"dht_nodes"`
	ConnectionStatus string `json:"connection_status"`
	URL              string `json:"url"`
	Error            string `json:"error,omitempty"`
}

// FetchQBStats 只读抓取 qB 状态：登录 → transfer/info → sync/maindata。
// 不做任何写入/删除种子的操作。url/username/password 任一为空返回 enabled=false。
func FetchQBStats(ctx context.Context, rawURL, username, password string) (QBStats, error) {
	if rawURL == "" || username == "" || password == "" {
		return QBStats{Enabled: false}, nil
	}
	base := strings.TrimRight(rawURL, "/")
	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{Timeout: 12 * time.Second, Jar: jar, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return nil }}

	form := url.Values{"username": {username}, "password": {password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return QBStats{Enabled: true, Error: err.Error()}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return QBStats{Enabled: true, Error: err.Error()}, err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 400 || strings.TrimSpace(string(body)) != "Ok." {
		msg := fmt.Sprintf("qb login failed: HTTP %d", resp.StatusCode)
		return QBStats{Enabled: true, Error: msg}, fmt.Errorf("%s", msg)
	}

	getJSON := func(path string, v interface{}) error {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return err
		}
		res, err := httpClient.Do(r)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode >= 400 {
			return fmt.Errorf("qb %s: HTTP %d", path, res.StatusCode)
		}
		return json.NewDecoder(res.Body).Decode(v)
	}

	var transfer struct {
		DLSpeed          int64  `json:"dl_info_speed"`
		UPSpeed          int64  `json:"up_info_speed"`
		DLTotal          int64  `json:"dl_info_data"`
		UPTotal          int64  `json:"up_info_data"`
		DHTNodes         int    `json:"dht_nodes"`
		ConnectionStatus string `json:"connection_status"`
	}
	if err := getJSON("/api/v2/transfer/info", &transfer); err != nil {
		return QBStats{Enabled: true, Error: err.Error()}, err
	}
	var maindata struct {
		Torrents map[string]struct {
			State string `json:"state"`
		} `json:"torrents"`
	}
	if err := getJSON("/api/v2/sync/maindata", &maindata); err != nil {
		return QBStats{Enabled: true, Error: err.Error()}, err
	}
	active := 0
	for _, t := range maindata.Torrents {
		if t.State != "pausedUP" && t.State != "pausedDL" {
			active++
		}
	}
	return QBStats{
		Enabled:          true,
		DLSpeed:          transfer.DLSpeed,
		UPSpeed:          transfer.UPSpeed,
		DLTotal:          transfer.DLTotal,
		UPTotal:          transfer.UPTotal,
		AllTorrents:      len(maindata.Torrents),
		ActiveTorrents:   active,
		DHTNodes:         transfer.DHTNodes,
		ConnectionStatus: transfer.ConnectionStatus,
		URL:              base,
	}, nil
}
