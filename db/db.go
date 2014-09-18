package db

import (
	"github.com/boltdb/bolt"
	"github.com/warik/gami"

	"github.com/warik/dialer/conf"
)

var db *bolt.DB
var dbStats bolt.Stats
var PutChan chan gami.Message
var DeleteChan chan gami.Message

func GetDB() *bolt.DB {
	return db
}

func GetStats() bolt.Stats {
	tStats := db.Stats()
	diff := tStats.Sub(&dbStats)
	dbStats = tStats
	return diff
}

func initDB() (db *bolt.DB) {
	db, err := bolt.Open(conf.CDR_DB_FILE, 0600, nil)
	if err != nil {
		panic(err)
	}
	if err := db.Update(func(tx *bolt.Tx) (err error) {
		_, err = tx.CreateBucketIfNotExists([]byte(conf.BOLT_CDR_BUCKET))
		return
	}); err != nil {
		panic(err)
	}
	dbStats = db.Stats()
	return
}

func init() {
	db = initDB()
	PutChan = make(chan gami.Message)
	DeleteChan = make(chan gami.Message)
}