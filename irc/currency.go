package irc

import (
	"botIAask/crypto"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ExchangeRates struct {
	Base  string             `json:"base"`
	Date  string             `json:"date"`
	Rates map[string]float64 `json:"rates"`
}

// currencyHTTP is shared across calls instead of building a new *http.Client per request.
var currencyHTTP = &http.Client{Timeout: 10 * time.Second}

// FetchRates retrieves current exchange rates for a given base currency.
func FetchRates(base string) (*ExchangeRates, error) {
	url := fmt.Sprintf("https://api.exchangerate-api.com/v4/latest/%s", base)
	resp, err := currencyHTTP.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch rates: %s", resp.Status)
	}

	var rates ExchangeRates
	if err := json.NewDecoder(resp.Body).Decode(&rates); err != nil {
		return nil, err
	}
	return &rates, nil
}

// ratesCacheTTL bounds how long a cachedFetchRates result is reused. Exchange rates
// don't move fast, and api.exchangerate-api.com's free tier saturates easily if every
// !euro/!peso/!convert message triggers a live call.
const ratesCacheTTL = 10 * time.Minute

var (
	ratesCacheMu sync.Mutex
	ratesCache   = map[string]struct {
		rates   *ExchangeRates
		fetched time.Time
	}{}
)

// cachedFetchRates serves FetchRates(base) from an in-memory cache when fresh, so a
// burst of currency commands (there are only ever two bases in practice: EUR, USD)
// makes at most one live request per ratesCacheTTL. A failed fetch is never cached, so
// a transient error doesn't lock in a failure for the TTL.
func cachedFetchRates(base string) (*ExchangeRates, error) {
	ratesCacheMu.Lock()
	if entry, ok := ratesCache[base]; ok && time.Since(entry.fetched) < ratesCacheTTL {
		ratesCacheMu.Unlock()
		return entry.rates, nil
	}
	ratesCacheMu.Unlock()

	rates, err := FetchRates(base)
	if err != nil {
		return nil, err
	}

	ratesCacheMu.Lock()
	ratesCache[base] = struct {
		rates   *ExchangeRates
		fetched time.Time
	}{rates: rates, fetched: time.Now()}
	ratesCacheMu.Unlock()
	return rates, nil
}

func (b *ircNetwork) handleEuroCommand(target string) {
	rates, err := cachedFetchRates("EUR")
	if err != nil {
		b.sendPrivmsg(target, fmt.Sprintf("Error fetching Euro rates: %v", err))
		return
	}

	usdRate, ok := rates.Rates["USD"]
	if !ok {
		b.sendPrivmsg(target, "USD rate not found in Euro data.")
		return
	}

	b.sendPrivmsg(target, fmt.Sprintf("\x0303,01[CURRENCY]\x03 1 EUR = %.4f USD", usdRate))
}

func (b *ircNetwork) handlePesoCommand(target string) {
	// Fetching USD as base to get USD/ARS and USD/EUR
	rates, err := cachedFetchRates("USD")
	if err != nil {
		b.sendPrivmsg(target, fmt.Sprintf("Error fetching currency rates: %v", err))
		return
	}

	arsRate, okars := rates.Rates["ARS"]
	eurRate, okeur := rates.Rates["EUR"]

	if !okars {
		b.sendPrivmsg(target, "ARS rate not found.")
		return
	}

	// 1 USD = arsRate ARS
	// 1 EUR = arsRate / eurRate ARS
	
	msg := fmt.Sprintf("\x0303,01[CURRENCY]\x03 1 USD = %.2f ARS", arsRate)
	if okeur && eurRate != 0 {
		eurToArs := arsRate / eurRate
		msg += fmt.Sprintf(" | 1 EUR = %.2f ARS", eurToArs)
	}

	b.sendPrivmsg(target, msg)
}

// parseConvertArgs parses "<amount> <from> <to>" (e.g. "100 usd ars") into an
// amount and uppercased currency codes. Pure/no I/O so it's directly testable.
func parseConvertArgs(rest string) (amount float64, from, to string, err error) {
	fields := strings.Fields(rest)
	if len(fields) != 3 {
		return 0, "", "", fmt.Errorf("expected 3 arguments, got %d", len(fields))
	}
	amount, err = strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, "", "", fmt.Errorf("invalid amount %q", fields[0])
	}
	return amount, strings.ToUpper(fields[1]), strings.ToUpper(fields[2]), nil
}

// formatConvertReply renders the !convert reply line. Pure/no I/O so it's directly testable.
func formatConvertReply(sender string, amount float64, from string, converted float64, to string) string {
	return fmt.Sprintf("\x0303,01[CURRENCY]\x03 %s: %.2f %s = %.2f %s", sender, amount, from, converted, to)
}

// handleConvertCommand: !convert <amount> <from> <to> — e.g. !convert 100 USD ARS.
func (b *ircNetwork) handleConvertCommand(target, sender, rest string) {
	amount, from, to, err := parseConvertArgs(rest)
	if err != nil {
		b.sendPrivmsg(target, fmt.Sprintf("Usage: %sconvert <amount> <from> <to> — e.g. %sconvert 100 USD ARS", b.pfx(), b.pfx()))
		return
	}

	rates, err := cachedFetchRates(from)
	if err != nil {
		b.sendPrivmsg(target, fmt.Sprintf("@%s: error fetching rates: %v", sender, err))
		return
	}
	rate, ok := rates.Rates[to]
	if !ok {
		b.sendPrivmsg(target, fmt.Sprintf("@%s: no rate found for %s -> %s", sender, from, to))
		return
	}
	b.sendPrivmsg(target, formatConvertReply(sender, amount, from, amount*rate, to))
}

func (b *ircNetwork) handleCryptoCommand(target string) {
	if b.cryptoDB == nil {
		b.sendPrivmsg(target, "Crypto database not initialized.")
		return
	}

	prices, err := b.cryptoDB.GetLatestPrices()
	if err != nil {
		b.sendPrivmsg(target, fmt.Sprintf("Error fetching crypto prices: %v", err))
		return
	}

	if len(prices) == 0 {
		b.sendPrivmsg(target, "No crypto data available yet. Background fetcher might be running.")
		return
	}

	// We want BTC, ETH prominently, then others.
	// The User said: "ethereum, bitcoin and top 5 crypto currencies values"
	
	var btc *crypto.PriceEntry
	var eth *crypto.PriceEntry
	var others []string

	for i := range prices {
		p := &prices[i]
		if p.Symbol == "BTC" {
			btc = p
		} else if p.Symbol == "ETH" {
			eth = p
		} else if len(others) < 5 {
			others = append(others, fmt.Sprintf("%s: $%.2f", p.Symbol, p.PriceUSD))
		}
	}

	var resultParts []string
	if btc != nil {
		resultParts = append(resultParts, fmt.Sprintf("\x0308,01BTC\x03: $%.2f", btc.PriceUSD))
	}
	if eth != nil {
		resultParts = append(resultParts, fmt.Sprintf("\x0302,01ETH\x03: $%.2f", eth.PriceUSD))
	}
	resultParts = append(resultParts, others...)

	msg := fmt.Sprintf("\x0313,01[CRYPTO]\x03 %s", strings.Join(resultParts, " | "))
	b.sendPrivmsg(target, msg)
}
