package phemex

import "net/url"

type Wallet struct {
	Currency               string `json:"currency"`
	BalanceEv              int    `json:"balanceEv"`
	LockedTradingBalanceEv int    `json:"lockedTradingBalanceEv"`
	LockedWithdrawEv       int    `json:"lockedWithdrawEv"`
	LastUpdateTimeNs       int64  `json:"lastUpdateTimeNs"`
}

type WalletResponse struct {
	Code int      `json:"code"`
	Msg  string   `json:"msg"`
	Data []Wallet `json:"data"`
}

func (c *Client) GetBalance(currency string) (*WalletResponse, error) {
	var res WalletResponse

	query := url.Values{}
	query.Add("currency", currency)
	if err := c.getPrivately("/spot/wallets", query, "", &res); err != nil {
		return nil, err
	}

	return &res, nil
}

func (c *Client) GetPositions() error {
	panic("unimplemented")
}
