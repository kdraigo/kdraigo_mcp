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

type openPosition struct {
	Side                 string     `json:"side"`
	Qty                  float64    `json:"qty"`
	AvgEntry             float64    `json:"avg_entry"`
	OpenedAt             *time.Time `json:"opened_at"`
	LastFillPrice        float64    `json:"last_fill_price"`
	UnrealizedAtLastFill float64    `json:"unrealized_at_last_fill"`
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
	OpenPosition   *openPosition  `json:"open_position"`
	Orders         orderBreakdown `json:"orders"`
}

type performanceReport struct {
	SessionID      string   `json:"session_id"`
	Quote          string   `json:"quote"`
	InitialCapital float64  `json:"initial_capital"`
	FinalEquity    float64  `json:"final_equity"`
	UnpricedAssets []string `json:"unpriced_assets"`
	Total          stats    `json:"total"`
	Instruments    []stats  `json:"instruments"`
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
