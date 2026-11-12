package irc

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"botIAask/internal/httpx"
	"botIAask/internal/ttlcache"
)

// Yahoo Finance's public, keyless (and unofficial) endpoints. Vars so tests can point them at httptest servers.
var (
	stockChartAPI  = "https://query1.finance.yahoo.com/v8/finance/chart/"
	stockSearchAPI = "https://query1.finance.yahoo.com/v1/finance/search?quotesCount=6&newsCount=0&q="
)

var stockHTTP = &http.Client{Timeout: 10 * time.Second}

// errStockNoData: Yahoo does not know the symbol (or has no bars for it).
var errStockNoData = errors.New("no data")

const stockTag = "\x0303,01[STOCK]\x03"

// Yahoo answers 429 to unidentified clients; a plain product token is enough.
const stockUA = "Mozilla/5.0 (compatible; botIAask)"

// stockTickerRe is what we accept as a literal Yahoo symbol when the name search finds nothing:
// NVDA, SAN.MC, ^GSPC, BTC-USD, EURUSD=X, BTC=F.
var stockTickerRe = regexp.MustCompile(`^[A-Za-z0-9^.=-]{1,15}$`)

// stockPeriod maps a user-facing period to a Yahoo range/interval; bars > 0 keeps only the last
// N bars of the fetched series (3d is cut from the 5d range).
type stockPeriod struct {
	label, rng, interval string
	bars                 int
}

var stockPeriods = map[string]stockPeriod{
	"now": {"now", "1d", "5m", 0},
	"1d":  {"1d", "1d", "5m", 0},
	"3d":  {"3d", "5d", "1d", 3},
	"5d":  {"5d", "5d", "1d", 0},
	"1m":  {"1m", "1mo", "1d", 0},
	"3m":  {"3m", "3mo", "1d", 0},
	"6m":  {"6m", "6mo", "1d", 0},
	"1y":  {"1y", "1y", "1d", 0},
}

var stockPeriodAlias = map[string]string{
	"today": "1d", "1w": "5d", "week": "5d", "1mo": "1m", "month": "1m", "3mo": "3m", "6mo": "6m", "year": "1y",
}

func lookupStockPeriod(s string) (stockPeriod, bool) {
	s = strings.ToLower(s)
	if a, ok := stockPeriodAlias[s]; ok {
		s = a
	}
	p, ok := stockPeriods[s]
	return p, ok
}

// parseStockArgs splits "<name or ticker words> [period]" (the period may also lead). Pure, no I/O.
func parseStockArgs(rest string) (query string, p stockPeriod) {
	f := strings.Fields(rest)
	p = stockPeriods["now"]
	if len(f) > 1 {
		if sp, ok := lookupStockPeriod(f[len(f)-1]); ok {
			p, f = sp, f[:len(f)-1]
		} else if sp, ok := lookupStockPeriod(f[0]); ok {
			p, f = sp, f[1:]
		}
	}
	return strings.Join(f, " "), p
}

// --- Yahoo wire format ---

type yahooChartResp struct {
	Chart struct {
		Result []struct {
			Meta struct {
				Symbol             string  `json:"symbol"`
				Currency           string  `json:"currency"`
				ExchangeName       string  `json:"exchangeName"`
				FullExchangeName   string  `json:"fullExchangeName"`
				LongName           string  `json:"longName"`
				ShortName          string  `json:"shortName"`
				PriceHint          int     `json:"priceHint"`
				RegularMarketPrice float64 `json:"regularMarketPrice"`
				ChartPreviousClose float64 `json:"chartPreviousClose"`
				DayHigh            float64 `json:"regularMarketDayHigh"`
				DayLow             float64 `json:"regularMarketDayLow"`
				High52             float64 `json:"fiftyTwoWeekHigh"`
				Low52              float64 `json:"fiftyTwoWeekLow"`
				Trading            struct {
					Regular struct {
						Start int64 `json:"start"`
						End   int64 `json:"end"`
					} `json:"regular"`
				} `json:"currentTradingPeriod"`
			} `json:"meta"`
			Indicators struct {
				Quote []struct {
					Close []*float64 `json:"close"`
					High  []*float64 `json:"high"`
					Low   []*float64 `json:"low"`
				} `json:"quote"`
			} `json:"indicators"`
		} `json:"result"`
	} `json:"chart"`
}

type stockBar struct{ c, h, l float64 }

// stockChart is one parsed chart response: the live quote plus the bars of the requested range.
type stockChart struct {
	Symbol, Name, Exchange, Currency string
	Hint                             int
	Price, PrevClose                 float64
	DayHigh, DayLow, High52, Low52   float64
	RegStart, RegEnd                 int64
	Bars                             []stockBar
}

func parseStockChart(r *yahooChartResp) (*stockChart, error) {
	if len(r.Chart.Result) == 0 || r.Chart.Result[0].Meta.RegularMarketPrice <= 0 {
		return nil, errStockNoData
	}
	res := r.Chart.Result[0]
	m := res.Meta
	c := &stockChart{
		Symbol: m.Symbol, Currency: m.Currency, Hint: m.PriceHint,
		Price: m.RegularMarketPrice, PrevClose: m.ChartPreviousClose,
		DayHigh: m.DayHigh, DayLow: m.DayLow, High52: m.High52, Low52: m.Low52,
		RegStart: m.Trading.Regular.Start, RegEnd: m.Trading.Regular.End,
		Name: m.LongName, Exchange: m.FullExchangeName,
	}
	if c.Name == "" {
		c.Name = m.ShortName
	}
	if c.Name == "" {
		c.Name = m.Symbol
	}
	if c.Exchange == "" {
		c.Exchange = m.ExchangeName
	}
	if len(res.Indicators.Quote) > 0 {
		q := res.Indicators.Quote[0]
		for i, cl := range q.Close {
			if cl == nil { // Yahoo leaves holes for bars with no trades
				continue
			}
			b := stockBar{c: *cl, h: *cl, l: *cl}
			if i < len(q.High) && q.High[i] != nil {
				b.h = *q.High[i]
			}
			if i < len(q.Low) && q.Low[i] != nil {
				b.l = *q.Low[i]
			}
			c.Bars = append(c.Bars, b)
		}
	}
	return c, nil
}

func stockGet(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", stockUA)
	resp, err := stockHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return httpx.DecodeJSON(resp.Body, httpx.MaxBody, v)
	case http.StatusNotFound, http.StatusBadRequest: // Yahoo: unknown symbol, or one it cannot parse
		return errStockNoData
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}

// --- lookup ---

type stockKey struct{ sym, rng string }

// Quotes live 1 minute (every period of one symbol shares the fetch of its range); name -> symbols
// answers barely change, so they are kept a day.
var (
	stockChartCache  = ttlcache.Cache[stockKey, *stockChart]{TTL: time.Minute, Max: 256}
	stockSearchCache = ttlcache.Cache[string, []stockCand]{TTL: 24 * time.Hour, Max: 512}
)

// stockCand is one search hit: a Yahoo symbol and its quoteType (EQUITY, ETF, INDEX, ...).
type stockCand struct{ sym, typ string }

// stockAlsoTypes are the hit types worth suggesting as "also:"; futures and currency pairs match
// almost any name ("SNVDA=F", "AMD=X") and would only be noise there.
var stockAlsoTypes = map[string]bool{"EQUITY": true, "ETF": true, "INDEX": true, "CRYPTOCURRENCY": true, "MUTUALFUND": true}

// stockResolve turns "nvidia" / "SAN.MC" / "bitcoin" into candidate Yahoo symbols, best first.
// A query typed exactly as a symbol ("AMD", "^GSPC") wins over the search ranking; lowercase words
// keep Yahoo's order, so "gold" is gold futures rather than the GOLD stock.
// The error is the search failure, if any; a literal-ticker candidate may still be returned with it.
func stockResolve(ctx context.Context, q string) ([]stockCand, error) {
	key := q // case-sensitive on purpose: "GOLD" (the symbol) and "gold" (the metal) rank differently
	if cands, ok := stockSearchCache.Get(key); ok {
		return cands, nil
	}
	var r struct {
		Quotes []struct {
			Symbol    string `json:"symbol"`
			QuoteType string `json:"quoteType"`
		} `json:"quotes"`
	}
	var cands []stockCand
	err := stockGet(ctx, stockSearchAPI+url.QueryEscape(q), &r)
	if err == nil {
		for _, h := range r.Quotes {
			if h.Symbol == "" || h.QuoteType == "OPTION" {
				continue
			}
			cands = append(cands, stockCand{h.Symbol, h.QuoteType})
			if h.Symbol == q {
				cands[0], cands[len(cands)-1] = cands[len(cands)-1], cands[0]
			}
		}
		if len(cands) > 0 {
			stockSearchCache.Put(key, cands)
		}
	}
	if len(cands) == 0 && stockTickerRe.MatchString(q) { // search empty or down: try it as a literal symbol
		cands = []stockCand{{sym: strings.ToUpper(q)}}
	}
	return cands, err
}

func stockFetch(ctx context.Context, sym string, p stockPeriod) (*stockChart, error) {
	key := stockKey{sym, p.rng}
	if c, ok := stockChartCache.Get(key); ok {
		return c, nil
	}
	var r yahooChartResp
	if err := stockGet(ctx, stockChartAPI+url.PathEscape(sym)+"?range="+p.rng+"&interval="+p.interval, &r); err != nil {
		return nil, err
	}
	c, err := parseStockChart(&r)
	if err != nil {
		return nil, err
	}
	if p.label != "now" && len(c.Bars) == 0 {
		return nil, errStockNoData
	}
	stockChartCache.Put(key, c)
	return c, nil
}

// stockLookup finds the best symbol for q (trying the next candidate if Yahoo has no data for the
// first) and renders the IRC line.
func stockLookup(ctx context.Context, q string, p stockPeriod, now time.Time) (string, error) {
	cands, serr := stockResolve(ctx, q)
	for i, cand := range cands {
		if i == 3 {
			break
		}
		c, err := stockFetch(ctx, cand.sym, p)
		if errors.Is(err, errStockNoData) {
			continue
		}
		if err != nil {
			return "", err
		}
		var also []string
		for _, o := range cands {
			if o.sym != cand.sym && stockAlsoTypes[o.typ] && len(also) < 3 {
				also = append(also, o.sym)
			}
		}
		return c.format(p, now, also), nil
	}
	if serr != nil { // nothing usable and the search itself failed: that is the real problem
		return "", serr
	}
	return "", errStockNoData
}

// --- formatting ---

func (c *stockChart) num(v float64) string {
	prec := c.Hint
	if prec <= 0 || prec > 8 {
		prec = 2
	}
	return fmt.Sprintf("%.*f", prec, v)
}

// change renders "▲ +1.23 (+0.74%)" in green/red/gray and returns that color for reuse.
func (c *stockChart) change(base float64) (color, text string) {
	if base <= 0 {
		return ircGray, ""
	}
	d := c.Price - base
	color, arrow := ircGray, "■"
	switch {
	case d > 0:
		color, arrow = ircLightGreen, "▲"
	case d < 0:
		color, arrow = ircRed, "▼"
	}
	prec := c.Hint
	if prec <= 0 || prec > 8 {
		prec = 2
	}
	return color, fmt.Sprintf("%s %+.*f (%+.2f%%)", arrow, prec, d, d/base*100)
}

// window returns the bars of the period and the close it is measured against: the close before
// the first bar (3d: the bar before the last three), or the previous close of the whole range.
func (c *stockChart) window(p stockPeriod) (bars []stockBar, base float64) {
	bars, base = c.Bars, c.PrevClose
	if p.bars > 0 && len(bars) > p.bars {
		base = bars[len(bars)-p.bars-1].c
		bars = bars[len(bars)-p.bars:]
	}
	if base <= 0 && len(bars) > 0 {
		base = bars[0].c
	}
	return bars, base
}

func (c *stockChart) format(p stockPeriod, now time.Time, also []string) string {
	var parts []string
	var head string
	if p.label == "now" {
		col, chg := c.change(c.PrevClose)
		head = fmt.Sprintf("%s %s (%s) %s %s", stockTag, c.Name, strings.Join(nonEmpty(c.Symbol, c.Exchange), " · "),
			ircBold+c.num(c.Price)+ircBold, c.Currency)
		if chg != "" {
			head += " " + col + chg + ircEnd + " today"
		}
		if c.DayLow > 0 && c.DayHigh > 0 {
			parts = append(parts, "day "+c.num(c.DayLow)+"–"+c.num(c.DayHigh))
		}
		if c.Low52 > 0 && c.High52 > 0 {
			parts = append(parts, "52w "+c.num(c.Low52)+"–"+c.num(c.High52))
		}
		if open := now.Unix() >= c.RegStart && now.Unix() <= c.RegEnd && c.RegEnd > 0; open {
			parts = append(parts, ircLightGreen+"market open"+ircEnd)
		} else {
			parts = append(parts, ircGray+"market closed"+ircEnd)
		}
	} else {
		bars, base := c.window(p)
		sum, low, high := 0.0, bars[0].l, bars[0].h
		closes := make([]float64, len(bars))
		for i, b := range bars {
			sum += b.c
			low, high = min(low, b.l), max(high, b.h)
			closes[i] = b.c
		}
		col, chg := c.change(base)
		head = fmt.Sprintf("%s %s (%s) %s %s → %s %s", stockTag, c.Name, c.Symbol, ircBoldLabel(p.label+":"),
			c.num(base), ircBold+c.num(c.Price)+ircBold, c.Currency)
		if chg != "" {
			head += " " + col + chg + ircEnd
		}
		parts = append(parts, "avg "+c.num(sum/float64(len(bars))), "low "+c.num(low)+" · high "+c.num(high),
			col+sparkline(closes, 16)+ircEnd)
	}
	if len(also) > 0 {
		parts = append(parts, ircGray+"also: "+strings.Join(also, ", ")+ircEnd)
	}
	return head + " | " + strings.Join(parts, " | ")
}

func nonEmpty(s ...string) []string {
	var out []string
	for _, x := range s {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

// sparkline draws v as block characters, thinned to at most width points (first and last kept).
func sparkline(v []float64, width int) string {
	if len(v) > width {
		pts := make([]float64, width)
		for i := range pts {
			pts[i] = v[i*(len(v)-1)/(width-1)]
		}
		v = pts
	}
	if len(v) == 0 {
		return ""
	}
	lo, hi := v[0], v[0]
	for _, x := range v {
		lo, hi = min(lo, x), max(hi, x)
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	var sb strings.Builder
	for _, x := range v {
		i := len(blocks) / 2
		if hi > lo {
			i = int((x-lo)/(hi-lo)*float64(len(blocks)-1) + 0.5)
		}
		sb.WriteRune(blocks[i])
	}
	return sb.String()
}

// handleStockCommand: !stock <name|ticker> [now|1d|3d|5d|1m|3m|6m|1y] (Yahoo Finance).
func (b *ircNetwork) handleStockCommand(target, sender, rest string) {
	q, p := parseStockArgs(rest)
	if q == "" || len(q) > 64 {
		b.sendPrivmsg(target, fmt.Sprintf("Usage: %sstock <name|ticker> [now|1d|3d|5d|1m|3m|6m|1y] — e.g. %sstock nvidia, %sstock apple 1m, %sstock ^GSPC 1y",
			b.pfx(), b.pfx(), b.pfx(), b.pfx()))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	line, err := stockLookup(ctx, q, p, time.Now())
	switch {
	case errors.Is(err, errStockNoData):
		line = fmt.Sprintf("@%s: stock: nothing found for %q — try the company name or its ticker (NVDA, SAN.MC, ^GSPC, BTC-USD, EURUSD=X)", sender, q)
	case err != nil:
		log.Printf("stock %q: %v", q, err)
		line = fmt.Sprintf("@%s: stock: Yahoo Finance unreachable", sender)
	}
	b.sendPrivmsg(target, b.sanitize(line))
}
