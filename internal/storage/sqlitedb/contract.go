package sqlitedb

import "time"

type Name string

const (
	Projection Name = "projection"
	Control    Name = "control"
)

type Options struct {
	ReadConns       int
	BusyTimeout     time.Duration
	SlowTxThreshold time.Duration
}
