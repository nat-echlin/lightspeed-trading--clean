package cls

import (
	"container/ring"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

// valid exchanges listed below

var Bybit = "bybit"
var Phemex = "phemex"
var ValidExchanges = []string{Bybit}

type Decimal = decimal.Decimal

type Exchange struct {
	Name       string
	SymbolData map[string]Symbol
	Clients    *ClientLoop
}

type Symbol struct {
	Name        string
	MaxLeverage Decimal
	QtyStep     Decimal
	MinQty      Decimal
	MaxQty      Decimal
	PriceStep   Decimal
}

func (s Symbol) String() string {
	return fmt.Sprintf("Name: %s, MaxLeverage: %s, QtyStep: %s, MinQty: %s, MaxQty: %s, PriceStep: %s",
		s.Name, s.MaxLeverage, s.QtyStep, s.MinQty, s.MaxQty, s.PriceStep)
}

// ManagedClient is a http client with ratelimit avoidance built in
type ManagedClient struct {
	httpClient   *http.Client
	requestTimes *ring.Ring // times that a request was made
}

type ClientLoop struct {
	Clients    []*ManagedClient
	numClients int
	mtx        sync.Mutex
	curInd     int
}

// get the next client and adds to the requestTimes of the client
func (cl *ClientLoop) next() *http.Client {
	cl.mtx.Lock()
	defer cl.mtx.Unlock()

	cl.curInd = (cl.curInd + 1) % cl.numClients

	cl.Clients[cl.curInd].requestTimes.Value = time.Now()
	cl.Clients[cl.curInd].requestTimes = cl.Clients[cl.curInd].requestTimes.Next()

	return cl.Clients[cl.curInd].httpClient
}

// get the next client to be used, add to the requestTimes of it
func (exc *Exchange) NextCli() *http.Client {
	return exc.Clients.next()
}

// NewExchange creates a new exchange with the specified name and a number of clients
func NewExchange(name string, clients []*http.Client, symbolData map[string]Symbol) *Exchange {
	// create the list of managed clients
	managedClients := make([]*ManagedClient, len(clients))
	for i, client := range clients {
		// initialize a new ring and set all its elements to the zero value of a time.Time
		r := ring.New(180)
		for j := 0; j < r.Len(); j++ {
			r.Value = time.Time{}
			r = r.Next()
		}
		managedClients[i] = &ManagedClient{
			httpClient:   client,
			requestTimes: r,
		}
	}

	// create and return pointer to an Exchange
	return &Exchange{
		Name:       name,
		SymbolData: symbolData,
		Clients: &ClientLoop{
			Clients:    managedClients,
			numClients: len(clients),
		},
	}
}
