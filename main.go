package main

import (
	"net"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/k-sone/critbitgo"
)

const (
	nlriAnnounce int = iota
	nlriWithdraw
)

type change struct {
	action int
	nlri   nlri
}

type nlri struct {
	prefix  net.IPNet
	nextHop net.IP
}

func main() {
	// Log Everything
	log.SetLevel(log.DebugLevel)

	// Track when we last ran an aggregation job
	lastUpdate := time.Now()

	// Create a BGP session for the full table input
	var peerInput Peer
	peerInput.Host = net.ParseIP("192.0.2.1")
	peerInput.Port = 179
	peerInput.LocalASN = 65000
	peerInput.HoldTime = 60 * time.Second
	peerInput.RouterID = 1234567890
	peerInput.RIB = critbitgo.NewNet()
	peerInput.Map = make(map[string][]net.IPNet)

	go func(peer *Peer) {
		for {
			if peer.State == 0 {
				peer.Dial()
			}
			time.Sleep(5 * time.Second)
		}
	}(&peerInput)

	// Create a BGP session for the aggregated table output
	var peerOutput Peer
	peerOutput.Host = net.ParseIP("192.0.2.2")
	peerOutput.Port = 179
	peerOutput.LocalASN = 65000
	peerOutput.HoldTime = 60 * time.Second
	peerOutput.RouterID = 1234567890
	peerOutput.RIB = critbitgo.NewNet()
	peerOutput.Map = make(map[string][]net.IPNet)

	go func(peer *Peer) {
		for {
			if peer.State == 0 {
				peer.Dial()
			}
			time.Sleep(5 * time.Second)
		}
	}(&peerOutput)

	// keep track of which prefixes we've exported
	var oldPrefixes []nlri

	for {
		if lastUpdate.Before(peerInput.Updated) {
			// duplicate the map to get a snapshot in time
			input := make(map[string]prefixes)
			for k, v := range peerInput.Map {
				input[k] = v
			}

			// this may take a while, so reset the clock now
			lastUpdate = time.Now()

			// process the current state of the input peer
			newPrefixes := aggregate(input)

			// work out differences between old and new tables
			changes := compare(newPrefixes, oldPrefixes)

			// submit changes to output peer
			for _, change := range changes {
				if change.action == nlriWithdraw {
					if err := peerOutput.Withdraw(change.nlri.prefix); err != nil {
						log.Warnf("Withdraw failed, err: %s", err)
					}
				} else if change.action == nlriAnnounce {
					if err := peerOutput.Announce(change.nlri.prefix, change.nlri.nextHop); err != nil {
						log.Warnf("Announce failed, err: %s", err)
					}
				} else {
					log.Fatal("Invalid change type received")
				}
			}

			// update the list of currently exported prefixes
			oldPrefixes = newPrefixes
		}

		// don't just thrash, rate limit how often we process things
		time.Sleep(5 * time.Second)
	}

}
