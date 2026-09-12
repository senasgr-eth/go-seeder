package cloudflare

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"time"
)

const apiBase = "https://api.cloudflare.com/client/v4"

type Client struct {
	email  string
	apiKey string
	http   *http.Client
}

func New(email, apiKey string) *Client {
	return &Client{
		email:  email,
		apiKey: apiKey,
		http:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) do(method, path string, body interface{}) ([]byte, error) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, apiBase+path, r)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" && len(c.apiKey) > 4 && c.apiKey[:4] == "cfat" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	} else {
		req.Header.Set("X-Auth-Email", c.email)
		req.Header.Set("X-Auth-Key", c.apiKey)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("cloudflare API %s %s: %s", method, path, string(data))
	}
	return data, nil
}

type cfResult struct {
	Success bool              `json:"success"`
	Result  []json.RawMessage `json:"result"`
}

type cfRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

func (c *Client) zoneID(domain string) (string, error) {
	data, err := c.do("GET", "/zones?name="+domain+"&status=active", nil)
	if err != nil {
		return "", err
	}
	var res struct {
		Result []struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return "", err
	}
	if len(res.Result) == 0 {
		return "", fmt.Errorf("zone not found: %s", domain)
	}
	return res.Result[0].ID, nil
}

func (c *Client) listRecords(zoneID, name, recType string) ([]cfRecord, error) {
	path := fmt.Sprintf("/zones/%s/dns_records?name=%s&type=%s", zoneID, name, recType)
	data, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	var res struct {
		Result []cfRecord `json:"result"`
	}
	json.Unmarshal(data, &res)
	return res.Result, nil
}

func (c *Client) createRecord(zoneID, name, recType, content string) error {
	body := map[string]interface{}{
		"type":    recType,
		"name":    name,
		"content": content,
		"ttl":     60,
		"proxied": false,
	}
	_, err := c.do("POST", "/zones/"+zoneID+"/dns_records", body)
	return err
}

func (c *Client) deleteRecord(zoneID, recordID string) error {
	_, err := c.do("DELETE", "/zones/"+zoneID+"/dns_records/"+recordID, nil)
	return err
}

// Sync updates Cloudflare DNS A/AAAA records for the given subdomain to match goodIPs.
// At most maxSeeds records are kept.
func (c *Client) Sync(domain, prefix string, goodIPs []netip.Addr, maxSeeds int) error {
	zoneID, err := c.zoneID(domain)
	if err != nil {
		return err
	}

	fqdn := prefix + "." + domain
	goodSet := make(map[string]struct{}, len(goodIPs))
	for _, ip := range goodIPs {
		goodSet[ip.String()] = struct{}{}
	}

	for _, recType := range []string{"A", "AAAA"} {
		existing, err := c.listRecords(zoneID, fqdn, recType)
		if err != nil {
			return err
		}

		// Remove stale records
		for _, rec := range existing {
			if _, ok := goodSet[rec.Content]; !ok {
				c.deleteRecord(zoneID, rec.ID)
			}
		}

		// Add new records up to maxSeeds
		existingSet := make(map[string]struct{})
		for _, rec := range existing {
			existingSet[rec.Content] = struct{}{}
		}

		count := len(existing)
		for _, ip := range goodIPs {
			if count >= maxSeeds {
				break
			}
			s := ip.String()
			if _, exists := existingSet[s]; exists {
				continue
			}
			isA := ip.Is4()
			if (recType == "A" && !isA) || (recType == "AAAA" && isA) {
				continue
			}
			if err := c.createRecord(zoneID, fqdn, recType, s); err == nil {
				count++
			}
		}
	}
	return nil
}
