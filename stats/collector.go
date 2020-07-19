package stats

import (
	"context"
	"sync"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/flexpool/solo/db"
	"github.com/flexpool/solo/log"

	"github.com/sirupsen/logrus"
	"github.com/syndtr/goleveldb/leveldb"
)

const statCollectionPeriodSecs = 60 // Collect stats every minute
const keepStatsForSecs = 86400      // Keep stats for one day

// Collector is a stat collection daemon struct
type Collector struct {
	// map[<worker-name>]PendingStat
	PendingStats map[string]PendingStat

	ShareDifficulty   uint64
	Database          *db.Database
	Context           context.Context
	ContextCancelFunc context.CancelFunc
	Mux               sync.Mutex
	engineWaitGroup   *sync.WaitGroup
}

// Init initializes the Collector
func (c *Collector) Init() {
	c.Mux.Lock()
	c.PendingStats = make(map[string]PendingStat)
	c.Mux.Unlock()
}

// Clear deletes all keys (and values) from the c.PendingStats
func (c *Collector) Clear() {
	c.Mux.Lock()
	for k := range c.PendingStats {
		delete(c.PendingStats, k)
	}
	c.Mux.Unlock()
}

// NewCollector creates a new Stats Collector
func NewCollector(database *db.Database, engineWaitGroup *sync.WaitGroup, shareDifficulty uint64) *Collector {
	ctx, cancelFunc := context.WithCancel(context.Background())
	c := Collector{
		Context:           ctx,
		ContextCancelFunc: cancelFunc,
		engineWaitGroup:   engineWaitGroup,
		Database:          database,
		ShareDifficulty:   shareDifficulty,
	}
	c.Init()
	return &c
}

// Run runs the StatsCollector
func (c *Collector) Run() {
	// Wait group
	c.engineWaitGroup.Add(1)
	defer c.engineWaitGroup.Done()

	prevCollectionTimestamp := time.Now().Unix() / statCollectionPeriodSecs * statCollectionPeriodSecs

	log.Logger.WithFields(logrus.Fields{
		"prefix": "stats",
	}).Info("Started Stats Collector")

	var totalCollectedHashrate float64

	for {
		select {
		case <-c.Context.Done():
			log.Logger.WithFields(logrus.Fields{
				"prefix": "stats",
			}).Info("Stopped Stats Collector")
			return
		default:
			currentCollectionTimestamp := time.Now().Unix() / statCollectionPeriodSecs * statCollectionPeriodSecs // Get rid of remainder
			if prevCollectionTimestamp == currentCollectionTimestamp {
				time.Sleep(time.Second)
				continue
			}

			prevCollectionTimestamp = currentCollectionTimestamp

			c.Mux.Lock()

			batch := new(leveldb.Batch)
			for workerName, pendingStat := range c.PendingStats {
				timestamp := time.Now().Unix() / statCollectionPeriodSecs * statCollectionPeriodSecs // Get rid of remainder
				effectiveHashrate := float64(pendingStat.ValidShares) * float64(c.ShareDifficulty)
				totalCollectedHashrate += effectiveHashrate / statCollectionPeriodSecs
				stat := db.Stat{
					WorkerName:        workerName,
					ValidShareCount:   pendingStat.ValidShares,
					StaleShareCount:   pendingStat.StaleShares,
					InvalidShareCount: pendingStat.InvalidShares,
					ReportedHashrate:  pendingStat.ReportedHashrate,
					EffectiveHashrate: effectiveHashrate,
					IPAddress:         pendingStat.IPAddress,
				}
				db.WriteStatToBatch(batch, stat, timestamp)
			}
			c.Mux.Unlock()

			c.Clear()

			c.Database.DB.Write(batch, nil)

			log.Logger.WithFields(logrus.Fields{
				"prefix":             "stats",
				"effective-hashrate": humanize.SIWithDigits(totalCollectedHashrate, 2, "H/s"),
			}).Info("Successfully collected data")
			totalCollectedHashrate = 0

			c.Database.PruneStats(keepStatsForSecs)
		}
	}
}

// Stop function stops stats collector
func (c *Collector) Stop() {
	c.ContextCancelFunc()
}
