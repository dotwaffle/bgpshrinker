package main

import (
	"log"
	"net"
	"sort"
)

func aggregate(input map[string]prefixes) []nlri {
	// somewhere to collect all our efforts before returning them
	var results []nlri

	// only aggregate as far as the Next Hops differ
	for nextHop, prefixes := range input {
		// to aggregate, we need a sorted set
		sort.Sort(prefixes)

		// if we don't make any changes on an aggregation run, consider it finished
		var changes = true
		for changes == true {
			// new run, no changes made yet
			changes = false

			for i := range prefixes {
				// does the next prefix and this prefix summarise to the same CIDR? If so, aggregate!
				if i < (len(prefixes) - 2) {
					shortened := shortenPrefixes(prefixes[i], prefixes[i+1])
					if len(shortened) == 1 {
						prefixes = append(shortened, prefixes[i+2:]...)
						changes = true
						break
					}
					merged := mergePrefixes(prefixes[i], prefixes[i+1])
					if len(merged) == 1 {
						prefixes = append(merged, prefixes[i+2:]...)
						changes = true
						break
					}
				}
			}

		}
		for _, prefix := range prefixes {
			results = append(results, nlri{prefix: prefix, nextHop: net.ParseIP(nextHop)})
		}
	}

	return results
}

func shortenPrefixes(left, right net.IPNet) []net.IPNet {
	// firstly, does either left fit in right, or right fit in left?
	if left.Contains(right.IP) || right.Contains(left.IP) {
		// whichever has the shorter mask is by default the winner!
		leftMask, leftBits := left.Mask.Size()
		rightMask, rightBits := right.Mask.Size()
		if leftMask < rightMask && leftBits == rightBits {
			return []net.IPNet{left}
		} else if rightMask > leftMask {
			return []net.IPNet{right}
		} else {
			log.Fatal("Identical Mask Conundrum!")
		}
	}

	// they didn't contain each other, so can't be aggregated!
	return []net.IPNet{left, right}
}

func mergePrefixes(left, right net.IPNet) []net.IPNet {
	// extract the netmasks
	leftMask, leftBits := left.Mask.Size()
	rightMask, rightBits := right.Mask.Size()

	// are the masks are the same size? if so, they're a candidate for merging
	if leftMask == rightMask && leftBits == rightBits {
		// does one size shorter fit both prefixes?
		shorterNet := net.IPNet{IP: left.IP, Mask: net.CIDRMask((leftMask - 1), leftBits)}
		if shorterNet.Contains(right.IP) {
			prefixes := make([]net.IPNet, 0)
			return append(prefixes, shorterNet)
		}
	}

	// they didn't merge with each other, so can't be aggregated!
	return []net.IPNet{left, right}
}
