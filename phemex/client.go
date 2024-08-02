package phemex

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	APIKey     string
	APISecret  string
	HTTPClient *http.Client
	BaseURL    string
}

func NewClient(testMode bool, key string, secret string) *Client {
	baseURL := "https://api.phemex.com"
	if testMode {
		baseURL = "https://testnet-api.phemex.com"
	}

	// if length is not divisible by 4 (needed for base64 url decoding), add = signs
	padding := len(secret) % 4
	if padding > 0 {
		secret += strings.Repeat("=", 4-padding)
	}

	return &Client{
		APIKey:     key,
		APISecret:  secret,
		HTTPClient: &http.Client{Timeout: time.Second * 10},
		BaseURL:    baseURL,
	}
}

type StandardResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// used on private, POST endpoints
func (c *Client) postJSON(path string, marshalledJSONBody []byte, dst any) error {
	// confirm client has auth
	if c.APIKey == "" || c.APISecret == "" {
		return fmt.Errorf("this is a private endpoint, please set API key and secret")
	}

	// create http request
	url := c.BaseURL + path
	req, err := http.NewRequest(http.MethodGet, url, bytes.NewBuffer(marshalledJSONBody))
	if err != nil {
		return fmt.Errorf("failed to create http request")
	}

	// get signature
	sign, err := c.signRequest(path, "", string(marshalledJSONBody))
	if err != nil {
		return fmt.Errorf("failed to get signature, %v", err)
	}

	// add headers
	expiry := strconv.FormatInt(time.Now().Unix()*1000, 10)
	req.Header.Add("x-phemex-request-expiry", expiry)
	req.Header.Add("x-phemex-request-signature", sign)
	req.Header.Add("x-phemex-access-token", c.APIKey)

	// make request
	if err := c.request(req, dst); err != nil {
		return fmt.Errorf("failed making request, %v", err)
	}

	return nil
}

func (c *Client) getPrivately(path string, query url.Values, body string, dst interface{}) error {
	if c.APIKey == "" || c.APISecret == "" {
		return fmt.Errorf("this is a private endpoint, please set API key and secret")
	}

	encodedQuery := query.Encode()
	signedQuery, err := c.signRequest(path, encodedQuery, body)
	if err != nil {
		return err
	}

	fullURL := c.BaseURL + path + "?" + encodedQuery
	req, err := http.NewRequest(http.MethodGet, fullURL, nil)
	if err != nil {
		return err
	}

	expiry := strconv.FormatInt(time.Now().Unix()*1000, 10)
	req.Header.Add("x-phemex-request-expiry", expiry)
	req.Header.Add("x-phemex-request-signature", signedQuery)
	req.Header.Add("x-phemex-access-token", c.APIKey)

	if err := c.request(req, dst); err != nil {
		return fmt.Errorf("failed making request, %v", err)
	}

	return nil
}

func (pc *Client) getPublicly(path string, query url.Values, dst interface{}) error {
	u, err := url.Parse(pc.BaseURL)
	if err != nil {
		return err
	}
	u.Path = path
	u.RawQuery = query.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}

	if err := pc.request(req, &dst); err != nil {
		return err
	}

	return nil
}

func (c *Client) signRequest(path string, queryString string, body string) (string, error) {
	expiry := strconv.FormatInt(time.Now().Unix()*1000, 10)
	stringToSign := path + queryString + expiry + body

	// decode the API secret from base64
	apiSecret, err := base64.URLEncoding.DecodeString(c.APISecret)
	if err != nil {
		return "", err
	}

	// create a new HMAC by defining the hash type and the key (as byte array)
	h := hmac.New(sha256.New, apiSecret)
	h.Write([]byte(stringToSign))

	// get result and encode as hexadecimal string
	signature := hex.EncodeToString(h.Sum(nil))

	return signature, nil
}

func (c *Client) request(req *http.Request, dst interface{}) error {
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to place request, %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read body, %v", err)
	}

	var standardResp StandardResp
	err = json.Unmarshal(bodyBytes, &standardResp)
	if err != nil {
		bodyString := string(bodyBytes)
		return fmt.Errorf("failed to marshall body: (%s) into standard response, %v", bodyString, err)
	}

	if resp.StatusCode != 200 || standardResp.Code != 0 {
		return fmt.Errorf("api error, status %d, error: %d %s", resp.StatusCode, standardResp.Code, standardResp.Msg)
	}

	if dst != nil {
		err = json.Unmarshal(bodyBytes, dst)
		if err != nil {
			return fmt.Errorf("failed to unmarshall into dst, %v", err)
		}
	}

	return nil
}
