package irc

import (
	"botIAask/crypto"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type ExchangeRates struct {
	Base  string             `json:"base"`
	Date  string             `json:"date"`
	Rates map[string]float64 `json:"rates"`
}

// FetchRates retrieves current exchange rates for a given base currency.
func FetchRates(base string) (*ExchangeRates, error) {
	url := fmt.Sprintf("https://api.exchangerate-api.com/v4/latest/%s", base)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
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

func (b *Bot) handleEuroCommand(target string) {
	rates, err := FetchRates("EUR")
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

func (b *Bot) handlePesoCommand(target string) {
	// Fetching USD as base to get USD/ARS and USD/EUR
	rates, err := FetchRates("USD")
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

func (b *Bot) handleCryptoCommand(target string) {
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
