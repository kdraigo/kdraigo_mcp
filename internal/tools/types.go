package tools

import "time"

// The backend's response bodies, decoded so the tools can render them. Only
// the fields a tool shows are here; the rest are ignored on decode.

// frontend_api /api/v1/dev/sessions and /sessions/:id.

type sessionProgress struct {
	ProcessedCandles int64      `json:"processed_candles"`
	TotalCandles     int64      `json:"total_candles"`
	Percent          float64    `json:"percent"`
	LastCandleTime   *time.Time `json:"last_candle_time"`
}

type sessionSummary struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Status     string           `json:"status"`
	Pair       string           `json:"pair"`
	Exchange   string           `json:"exchange"`
	Exchanges  []string         `json:"exchanges"`
	Timeframe  string           `json:"timeframe"`
	IsFavorite bool             `json:"is_favorite"`
	Tags       []string         `json:"tags"`
	CreatedAt  time.Time        `json:"created_at"`
	Progress   *sessionProgress `json:"progress"`
}

type wallet struct {
	Exchange      string  `json:"exchange"`
	Asset         string  `json:"asset"`
	Balance       float64 `json:"balance"`
	LockedBalance float64 `json:"locked_balance"`
}

type instrument struct {
	Exchange   string     `json:"exchange"`
	Pair       string     `json:"pair"`
	OrderCount int        `json:"order_count"`
	Filled     int        `json:"filled"`
	Buys       int        `json:"buys"`
	Sells      int        `json:"sells"`
	Notional   float64    `json:"notional"`
	Fees       float64    `json:"fees"`
	FeeAsset   string     `json:"fee_asset"`
	AvgBuy     *float64   `json:"avg_buy"`
	AvgSell    *float64   `json:"avg_sell"`
	FirstFill  *time.Time `json:"first_fill"`
	LastFill   *time.Time `json:"last_fill"`
}

type sessionDetail struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Status      string           `json:"status"`
	Pair        string           `json:"pair"`
	Exchange    string           `json:"exchange"`
	Exchanges   []string         `json:"exchanges"`
	Timeframe   string           `json:"timeframe"`
	IsFavorite  bool             `json:"is_favorite"`
	Notes       string           `json:"notes"`
	Tags        []string         `json:"tags"`
	CreatedAt   time.Time        `json:"created_at"`
	Progress    *sessionProgress `json:"progress"`
	Wallets     []wallet         `json:"wallets"`
	StartAt     *time.Time       `json:"start_at"`
	EndAt       *time.Time       `json:"end_at"`
	Instruments []instrument     `json:"instruments"`
}

// frontend_api /sessions/:id/positions/page and /funding/page.

type position struct {
	Exchange       string     `json:"exchange"`
	Pair           string     `json:"pair"`
	Side           string     `json:"side"`
	Size           float64    `json:"size"`
	EntryPrice     float64    `json:"entry_price"`
	Leverage       float64    `json:"leverage"`
	IsolatedMargin float64    `json:"isolated_margin"`
	RealizedPnL    float64    `json:"realized_pnl"`
	FundingPaid    float64    `json:"funding_paid"`
	ClosedAt       *time.Time `json:"closed_at"`
	Liquidated     bool       `json:"liquidated"`
	OpenedAt       time.Time  `json:"opened_at"`
}

type positionsPage struct {
	Positions  []position `json:"positions"`
	Total      int        `json:"total"`
	Limit      int        `json:"limit"`
	Offset     int        `json:"offset"`
	Open       int        `json:"open"`
	Liquidated int        `json:"liquidated"`
}

type fundingPayment struct {
	Exchange     string    `json:"exchange"`
	Pair         string    `json:"pair"`
	FundingTime  time.Time `json:"funding_time"`
	Rate         float64   `json:"rate"`
	PositionSize float64   `json:"position_size"`
	MarkPrice    float64   `json:"mark_price"`
	MarkSource   string    `json:"mark_source"`
	Amount       float64   `json:"amount"`
}

type fundingPage struct {
	Payments    []fundingPayment `json:"payments"`
	Total       int              `json:"total"`
	Limit       int              `json:"limit"`
	Offset      int              `json:"offset"`
	Paid        int              `json:"paid"`
	AmountTotal float64          `json:"amount_total"`
}

// frontend_api /orders.

type order struct {
	ID         string         `json:"id"`
	Exchange   string         `json:"exchange"`
	Pair       string         `json:"pair"`
	Type       string         `json:"type"`
	Side       string         `json:"side"`
	Amount     float64        `json:"amount"`
	Price      float64        `json:"price"`
	Status     string         `json:"status"`
	Fee        float64        `json:"fee"`
	FeeAsset   string         `json:"fee_asset"`
	FilledQty  float64        `json:"filled_qty"`
	AvgPrice   float64        `json:"avg_price"`
	StopPrice  *float64       `json:"stop_price"`
	GroupID    string         `json:"group_id"`
	Role       string         `json:"role"`
	Reason     map[string]any `json:"reason"`
	Logs       []string       `json:"logs"`
	CreatedAt  time.Time      `json:"created_at"`
	ExecutedAt time.Time      `json:"executed_at"`
}

type ordersPage struct {
	Orders []order `json:"orders"`
	Total  int     `json:"total"`
	Limit  int     `json:"limit"`
	Offset int     `json:"offset"`
}

// analytics_service /analytics/performance and /analytics/series.

type orderTally struct {
	Placed   int `json:"placed"`
	Filled   int `json:"filled"`
	Canceled int `json:"canceled"`
	Rejected int `json:"rejected"`
}

type orderBreakdown struct {
	Total       orderTally `json:"total"`
	Market      orderTally `json:"market"`
	Limit       orderTally `json:"limit"`
	StopLoss    orderTally `json:"stop_loss"`
	TakeProfit  orderTally `json:"take_profit"`
	Liquidation orderTally `json:"liquidation"`
	Other       orderTally `json:"other"`
}

// openBlock is what was still open when the run stopped (report version 3):
// left out of every figure, valued at the instruments' last close.
type openBlock struct {
	Positions []openPosition `json:"positions"`
	// Unrealized is nil when any position has no price.
	Unrealized *float64 `json:"unrealized"`
	Realized   float64  `json:"realized"`
	Fees       float64  `json:"fees"`
	Funding    float64  `json:"funding"`
}

type openPosition struct {
	Exchange string     `json:"exchange"`
	Pair     string     `json:"pair"`
	Side     string     `json:"side"`
	Qty      float64    `json:"qty"`
	AvgEntry float64    `json:"avg_entry"`
	OpenedAt *time.Time `json:"opened_at"`
	// MarkPrice is the last close the engine recorded at the end, nil
	// before it did; Unrealized is the position's value change at it.
	MarkPrice  *float64   `json:"mark_price"`
	MarkAt     *time.Time `json:"mark_at"`
	Unrealized *float64   `json:"unrealized"`
	Realized   float64    `json:"realized"`
	Fees       float64    `json:"fees"`
	Funding    float64    `json:"funding"`

	// lookedUp: the price came from market data here, the engine had none.
	lookedUp bool
}

type stats struct {
	Exchange       string         `json:"exchange"`
	Pair           string         `json:"pair"`
	Model          string         `json:"model"`
	NetPnL         float64        `json:"net_pnl"`
	ReturnPct      *float64       `json:"return_pct"`
	GrossPnL       float64        `json:"gross_pnl"`
	Fees           float64        `json:"fees"`
	Funding        float64        `json:"funding"`
	MaxDrawdown    float64        `json:"max_drawdown"`
	MaxDrawdownPct *float64       `json:"max_drawdown_pct"`
	Trades         int            `json:"trades"`
	Wins           int            `json:"wins"`
	Losses         int            `json:"losses"`
	WinRate        *float64       `json:"win_rate"`
	ProfitFactor   *float64       `json:"profit_factor"`
	AvgWin         *float64       `json:"avg_win"`
	AvgLoss        *float64       `json:"avg_loss"`
	BestTrade      *float64       `json:"best_trade"`
	WorstTrade     *float64       `json:"worst_trade"`
	LongTrades     int            `json:"long_trades"`
	ShortTrades    int            `json:"short_trades"`
	LongWins       int            `json:"long_wins"`
	ShortWins      int            `json:"short_wins"`
	Liquidations   int            `json:"liquidations"`
	OpenAtEnd      int            `json:"open_at_end"`
	Orders         orderBreakdown `json:"orders"`

	// Report version 3: nil when everything closed.
	Open *openBlock `json:"open"`

	// Report version 2.
	ClosedAtEnd           int       `json:"closed_at_end"`
	ReturnToDD            *float64  `json:"return_to_dd"`
	Expectancy            *float64  `json:"expectancy"`
	AvgR                  *float64  `json:"avg_r"`
	RSE                   *float64  `json:"r_se"`
	SumR                  *float64  `json:"sum_r"`
	RTrades               int       `json:"r_trades"`
	WinnersOver5R         int       `json:"winners_over_5r"`
	NetWithoutTop5        *float64  `json:"net_without_top5"`
	Top5Share             *float64  `json:"top5_share"`
	LongestLosingStreak   int       `json:"longest_losing_streak"`
	LongestUnderwaterDays *float64  `json:"longest_underwater_days"`
	AvgHoldSec            *float64  `json:"avg_hold_sec"`
	ExposurePct           *float64  `json:"exposure_pct"`
	Months                int       `json:"months"`
	LosingMonths          int       `json:"losing_months"`
	WorstMonth            *monthNet `json:"worst_month"`
}

type monthNet struct {
	Key string  `json:"key"`
	Net float64 `json:"net"`
}

type ratios struct {
	Days    int      `json:"days"`
	Sharpe  *float64 `json:"sharpe"`
	Sortino *float64 `json:"sortino"`
	CAGR    *float64 `json:"cagr_pct"`
	Calmar  *float64 `json:"calmar"`
}

type reportWarning struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type performanceReport struct {
	SessionID      string   `json:"session_id"`
	Quote          string   `json:"quote"`
	InitialCapital float64  `json:"initial_capital"`
	FinalEquity    float64  `json:"final_equity"`
	UnpricedAssets []string `json:"unpriced_assets"`
	Total          stats    `json:"total"`
	Instruments    []stats  `json:"instruments"`

	// Report version 2; zero from an older analytics_service, which has
	// none of what follows.
	ReportVersion       int             `json:"report_version"`
	InstrumentsPositive int             `json:"instruments_positive"`
	Ratios              ratios          `json:"ratios"`
	Warnings            []reportWarning `json:"warnings"`

	// The account's value at each day's close, open positions included, and
	// buy and hold over the window; nil where the engine did not record the
	// days (live sessions, and backtests before it did).
	MarkToMarket *markToMarket `json:"mark_to_market"`
	Benchmark    *benchmark    `json:"benchmark"`
}

type markToMarket struct {
	FinalEquity    float64  `json:"final_equity"`
	ReturnPct      *float64 `json:"return_pct"`
	MaxDrawdown    float64  `json:"max_drawdown"`
	MaxDrawdownPct *float64 `json:"max_drawdown_pct"`
	Curve          []struct {
		Day    string  `json:"day"`
		Equity float64 `json:"equity"`
	} `json:"curve"`
}

type benchmark struct {
	Instruments []struct {
		Exchange       string  `json:"exchange"`
		Pair           string  `json:"pair"`
		FirstOpen      float64 `json:"first_open"`
		LastClose      float64 `json:"last_close"`
		ReturnPct      float64 `json:"return_pct"`
		MaxDrawdownPct float64 `json:"max_drawdown_pct"`
	} `json:"instruments"`
	BasketReturnPct *float64 `json:"basket_return_pct"`
	ExcessPct       *float64 `json:"excess_pct"`
}

// analytics_service /analytics/trades and /analytics/breakdown (report
// version 2).

type trade struct {
	ID           int        `json:"id"`
	Exchange     string     `json:"exchange"`
	Pair         string     `json:"pair"`
	Side         string     `json:"side"`
	EntryAt      time.Time  `json:"entry_at"`
	ExitAt       *time.Time `json:"exit_at"`
	HoldSec      int64      `json:"hold_sec"`
	EntryPx      float64    `json:"entry_px"`
	ExitPx       *float64   `json:"exit_px"`
	QtyEntry     float64    `json:"qty_entry"`
	QtyPeak      float64    `json:"qty_peak"`
	Adds         int        `json:"adds"`
	Partials     int        `json:"partials"`
	Gross        float64    `json:"gross"`
	Fees         float64    `json:"fees"`
	Funding      float64    `json:"funding"`
	Net          float64    `json:"net"`
	StopPx       *float64   `json:"stop_px"`
	FinalStopPx  *float64   `json:"final_stop_px"`
	Risk         *float64   `json:"risk"`
	NetR         *float64   `json:"net_r"`
	GrossR       *float64   `json:"gross_r"`
	RSource      string     `json:"r_source"`
	ExitKind     string     `json:"exit_kind"`
	Liquidated   bool       `json:"liquidated"`
	OpenAtEnd    bool       `json:"open_at_end"`
	Unrealized   *float64   `json:"unrealized"`
	ClosedAtEnd  bool       `json:"closed_at_end"`
	Inherited    bool       `json:"inherited"`
	Reconciled   *bool      `json:"reconciled"`
	EntryOrderID string     `json:"entry_order_id"`
	ExitOrderID  string     `json:"exit_order_id"`
}

type tradesPage struct {
	ReportVersion int             `json:"report_version"`
	Total         int             `json:"total"`
	Limit         int             `json:"limit"`
	Offset        int             `json:"offset"`
	Trades        []trade         `json:"trades"`
	Warnings      []reportWarning `json:"warnings"`
}

type bucket struct {
	Key          string   `json:"key"`
	Trades       int      `json:"trades"`
	Wins         int      `json:"wins"`
	WinRate      *float64 `json:"win_rate"`
	Net          float64  `json:"net"`
	Gross        float64  `json:"gross"`
	Fees         float64  `json:"fees"`
	Funding      float64  `json:"funding"`
	ProfitFactor *float64 `json:"pf"`
	AvgR         *float64 `json:"avg_r"`
	RSE          *float64 `json:"r_se"`
	SumR         *float64 `json:"sum_r"`
	RTrades      int      `json:"r_trades"`
}

type breakdownResponse struct {
	By      string   `json:"by"`
	At      string   `json:"at"`
	Buckets []bucket `json:"buckets"`
	Total   bucket   `json:"total"`
}

type seriesPoint struct {
	OpenTime int64   `json:"openTime"` // Unix ms
	Value    float64 `json:"value"`
}

type seriesResponse struct {
	Points    []seriesPoint `json:"points"`
	Metric    string        `json:"metric"`
	Timeframe string        `json:"timeframe"`
}
