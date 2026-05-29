package main

// TrackedPosition represents a position being monitored for exit detection
type TrackedPosition struct {
	ID            int64
	Wallet        string
	MarketSlug    string
	MarketTitle   string
	TokenType     string // YES or NO
	TrackedAmount float64
	EntryScore    int
	Status        string // active / exited
	CreatedAt     string
	UpdatedAt     string
}

// TrackContext holds alert data temporarily for callback resolution (TTL 10min)
type TrackContext struct {
	Wallet      string
	MarketSlug  string
	MarketTitle string
	TokenType   string
	Amount      float64
	Score       int
}
