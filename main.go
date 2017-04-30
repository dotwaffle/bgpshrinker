package main

import (
	"flag"
	"io/ioutil"
	"net"
	"time"

	yaml "gopkg.in/yaml.v2"

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

// Configuration is read in through a YAML configuration file
type Configuration struct {
	Input  Peer
	Output Peer
}

var (
	configFileInput  = flag.String("config-input", "bgpshrinker.input.conf", "Path to configuration file for input session")
	configFileOutput = flag.String("config-output", "bgpshrinker.output.conf", "Path to configuration file for output session")
)

func main() {
	// Log Everything
	log.SetLevel(log.DebugLevel)

	// Read Flags
	flag.Parse()

	// Read Configuration Files
	var peerInput Peer
	rawConfigInput, err := ioutil.ReadFile(*configFileInput)
	if err != nil {
		log.Fatalf("Could not read config file, err: %v", err)
	}
	if err := yaml.Unmarshal(rawConfigInput, &peerInput); err != nil {
		log.Fatalf("Could not parse JSON/YAML inside config file, err: %v", err)
	}

	var peerOutput Peer
	rawConfigOutput, err := ioutil.ReadFile(*configFileOutput)
	if err != nil {
		log.Fatalf("Could not read config file, err: %v", err)
	}
	if err := yaml.Unmarshal(rawConfigOutput, &peerOutput); err != nil {
		log.Fatalf("Could not parse JSON/YAML inside config file, err: %v", err)
	}

	// Track when we last ran an aggregation job
	lastUpdate := time.Now()

	// Create a BGP session for the full table input
	peerInput.rib = critbitgo.NewNet()
	peerInput.mapNextHops = make(map[string][]net.IPNet)

	// Create a BGP session for the aggregated table output
	peerOutput.rib = critbitgo.NewNet()
	peerOutput.mapNextHops = make(map[string][]net.IPNet)

	// Start the input side BGP session
	go func(peer *Peer) {
		for {
			if peer.state == bgpStateIDLE {
				peer.Dial()
			}

			// hold, don't flood the speaker with logs of attempts if it fails
			time.Sleep(5 * time.Second)
		}
	}(&peerInput)

	// Start the output side BGP session
	go func(peer *Peer) {
		for {
			if peer.state == bgpStateIDLE {
				peer.Dial()
			}

			// hold, don't flood the speaker with logs of attempts if it fails
			time.Sleep(5 * time.Second)
		}
	}(&peerOutput)

	// keep track of which prefixes we've exported
	var oldPrefixes []nlri

	for {
		if lastUpdate.Before(peerInput.updated) {
			// duplicate the map to get a snapshot in time
			input := make(map[string]prefixes)
			for k, v := range peerInput.mapNextHops {
				input[k] = v
			}

			// this may take a while, so reset the clock now
			lastUpdate = time.Now()

			// process the current state of the input peer
			newPrefixes := aggregate(input)

			// work out differences between old and new tables
			changes := diffSets(newPrefixes, oldPrefixes)

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
